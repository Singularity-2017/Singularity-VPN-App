//go:build windows

package windows

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// Windows asks every adapter's resolvers at once and takes the first
// answer, so with the tunnel up a name is still answered by the router as
// often as not. For AntiZapret that is the whole difference: the node's
// resolver maps a listed name to an address the tunnel carries, the
// router answers with the real one, which goes direct -- seen on the
// owner's machine 2026-10-21 as claude.ai answered 198.19.x by the node
// and 160.79.x by the router, and refusing the connection as Russian.
// A Name Resolution Policy Table rule for "." sends every name to the
// tunnel's resolvers alone. A more specific rule another program set
// (Tailscale's ".ts.net") still wins for its own names.

// dns_policy_key is this service's one NRPT rule, under a fixed name so
// that a service which died with the rule in place removes it at its next
// start instead of leaving every name pointed at a resolver nothing
// reaches. Local rules are ignored where Group Policy sets NRPT rules of
// its own; a machine in a domain like that keeps the leak.
const dns_policy_key = `SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig\{B1E296B6-B632-414F-B8BD-A173E2E55205}`

// dns_policy_owner is the adapter whose resolvers the rule names. A switch
// brings the new tunnel up before the old one is cleared, and they can be
// different adapters (WireGuard and AmneziaWG are), so the old one's Clear
// must not take away the rule the new one just set.
var (
	dns_policy_mu    sync.Mutex
	dns_policy_owner winipcfg.LUID
)

// set_dns_policy points every name at servers, on behalf of owner.
func set_dns_policy(owner winipcfg.LUID, servers []netip.Addr) error {
	dns_policy_mu.Lock()
	defer dns_policy_mu.Unlock()

	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, dns_policy_key, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("platform/windows: creating the DNS policy rule: %w", err)
	}
	// Name goes last: until it is there the rule matches nothing, so the
	// DNS client never sees a catch-all with no servers yet.
	values := []error{
		key.SetDWordValue("Version", 2),
		key.SetStringValue("GenericDNSServers", dns_servers_value(servers)),
		key.SetDWordValue("ConfigOptions", 8), // the rule names generic DNS servers
		key.SetStringValue("IPSECCARestriction", ""),
		key.SetStringValue("Comment", "Singularity VPN"),
		key.SetStringsValue("Name", []string{"."}),
	}
	key.Close()
	if err := errors.Join(values...); err != nil {
		// Half a rule has no owner Clear would recognise; it goes now.
		return errors.Join(fmt.Errorf("platform/windows: writing the DNS policy rule: %w", err), delete_dns_policy())
	}
	dns_policy_owner = owner
	flush_resolver_cache()
	return nil
}

// clear_dns_policy removes the rule if owner set it.
func clear_dns_policy(owner winipcfg.LUID) error {
	dns_policy_mu.Lock()
	defer dns_policy_mu.Unlock()
	if dns_policy_owner != owner {
		return nil
	}
	dns_policy_owner = 0
	return delete_dns_policy()
}

// Remove_dns_policy removes the rule whoever set it. The service calls it
// at start, when no tunnel of its own can be up.
func Remove_dns_policy() error {
	dns_policy_mu.Lock()
	defer dns_policy_mu.Unlock()
	dns_policy_owner = 0
	return delete_dns_policy()
}

func delete_dns_policy() error {
	err := registry.DeleteKey(registry.LOCAL_MACHINE, dns_policy_key)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("platform/windows: removing the DNS policy rule: %w", err)
	}
	flush_resolver_cache()
	return nil
}

// dns_servers_value is GenericDNSServers: the addresses joined by "; ".
func dns_servers_value(servers []netip.Addr) string {
	out := make([]string, len(servers))
	for i, s := range servers {
		out[i] = s.String()
	}
	return strings.Join(out, "; ")
}

var proc_flush_resolver_cache = windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache")

// flush_resolver_cache drops answers cached under the rule that held
// before, which would otherwise keep sending a name the old way until they
// expire. A failure only means they expire on their own.
func flush_resolver_cache() {
	if proc_flush_resolver_cache.Find() == nil {
		proc_flush_resolver_cache.Call()
	}
}
