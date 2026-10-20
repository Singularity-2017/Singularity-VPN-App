package wireguard

import (
	"fmt"
	"net/netip"
	"strings"
)

// Uapi_set renders the "set=1" configuration payload for wireguard-go's
// Device.IpcSet. Device-level lines come first, then one block per peer,
// each opening with public_key -- that is what the library's UAPI parser
// (device/uapi.go) uses to recognise the start of a new peer.
//
// endpoints is parallel to Peers: the address each peer's Endpoint resolved
// to. It is resolved exactly once, by the caller, before the tunnel comes
// up. This package is pure and cannot resolve a hostname itself, and the
// library's default bind parses an endpoint with netip.ParseAddrPort and
// does not resolve hostnames either, so somebody has to -- and it must not
// be deferred until after the tunnel is up: a full-tunnel configuration
// routes DNS through the tunnel, so a hostname looked up at that moment is
// looked up through the very tunnel that needs the answer to exist first.
// Resolve first, keep the address, use it here and for the host route in
// internal/routing.
func (t Tunnel_config) Uapi_set(endpoints []netip.AddrPort) (string, error) {
	if len(endpoints) != len(t.Peers) {
		return "", fmt.Errorf("wireguard: %d endpoints for %d peers", len(endpoints), len(t.Peers))
	}
	for _, endpoint := range endpoints {
		if !endpoint.IsValid() {
			return "", fmt.Errorf("wireguard: invalid resolved endpoint %v", endpoint)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", t.Private_key.Hex())
	b.WriteString("replace_peers=true\n")
	// AmneziaWG parameters are device-level lines and so come before the
	// first public_key; amneziawg-go reads them under the same names.
	for _, param := range t.Obfuscation {
		fmt.Fprintf(&b, "%s=%s\n", param.Key, param.Value)
	}

	for i, peer := range t.Peers {
		fmt.Fprintf(&b, "public_key=%s\n", peer.Public_key.Hex())
		if peer.Has_preshared {
			fmt.Fprintf(&b, "preshared_key=%s\n", peer.Preshared_key.Hex())
		}
		fmt.Fprintf(&b, "endpoint=%s\n", endpoints[i].String())
		if peer.Keepalive > 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", int(peer.Keepalive.Seconds()))
		}
		b.WriteString("replace_allowed_ips=true\n")
		for _, allowed := range peer.Allowed_ips {
			fmt.Fprintf(&b, "allowed_ip=%s\n", allowed.String())
		}
	}

	return b.String(), nil
}
