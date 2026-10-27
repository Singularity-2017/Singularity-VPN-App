//go:build windows

package windows

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// Adapter_summary describes one network adapter that is up, for a problem
// report: what kind it is, whether it carries the default route, and what
// class of address it has. It never holds an address of the machine itself,
// nor a hardware address, nor the name of the Wi-Fi network joined -- a report
// is read by people the person does not know. A resolver is the exception only
// when it is a public service far from the machine: a resolver on the
// machine's own network, or the router, is described by class like any
// address, since where it is says where the person is, and an ISP's resolver
// says who their provider is only when it is a public one anyone may use.
type Adapter_summary struct {
	Name        string `json:"name"`        // friendly name ("Wi-Fi", "ZeroTier One"), without bracketed identifiers
	Description string `json:"description"` // driver description
	// Kind is wifi, ethernet, mobile, tunnel, loopback or other.
	Kind     string `json:"kind"`
	Hardware bool   `json:"hardware"` // a physical adapter, not a virtual one
	// Mtu is absent when Windows does not know it: the loopback adapter
	// answers 0xFFFFFFFF, which is not a size.
	Mtu uint32 `json:"mtu,omitempty"`
	// Default says the adapter carries a default route, IPv4 or IPv6, or
	// the two halves of one.
	Default bool `json:"default"`
	// Ipv4 and Ipv6 hold one class per address: private, cgnat, link-local,
	// public, loopback; ula, link-local, global, loopback.
	Ipv4    []string `json:"ipv4,omitempty"`
	Ipv6    []string `json:"ipv6,omitempty"`
	Gateway bool     `json:"gateway"` // the adapter has a gateway address
	// Dns holds one entry per DNS server: the address of a public resolver
	// outside the machine's own networks, the class of any other.
	Dns []string `json:"dns,omitempty"`
}

// The address classes.
const (
	class_private    = "private"
	class_cgnat      = "cgnat"
	class_link_local = "link-local"
	class_public     = "public"
	class_loopback   = "loopback"
	class_ula        = "ula"
	class_site_local = "site-local"
	class_global     = "global"
)

var (
	cgnat_range = netip.MustParsePrefix("100.64.0.0/10")
	// site_local_range is IPv6's deprecated site-local range, which Windows
	// still lists as the DNS servers of an adapter that has none.
	site_local_range = netip.MustParsePrefix("fec0::/10")
)

// address_class names what kind of address ip is, for a report that must not
// carry the address. It is empty for an address no adapter is worth
// describing by: unspecified or multicast.
func address_class(ip netip.Addr) string {
	ip = ip.Unmap()
	switch {
	case !ip.IsValid(), ip.IsUnspecified(), ip.IsMulticast():
		return ""
	case ip.IsLoopback():
		return class_loopback
	case ip.IsLinkLocalUnicast():
		return class_link_local
	case ip.Is4():
		switch {
		case cgnat_range.Contains(ip):
			return class_cgnat
		case ip.IsPrivate():
			return class_private
		}
		return class_public
	case ip.IsPrivate():
		// fc00::/7, the only private range IPv6 has.
		return class_ula
	case site_local_range.Contains(ip):
		return class_site_local
	}
	return class_global
}

// dns_entry is how a resolver appears in the report: its address when it is
// public and outside own, the prefixes of the machine's own addresses -- a
// resolver there is on the person's network, or is their router -- and
// otherwise its class. It is empty for an address with no class.
func dns_entry(ip netip.Addr, own []netip.Prefix) string {
	ip = ip.Unmap()
	class := address_class(ip)
	if class != class_public && class != class_global {
		return class
	}
	for _, prefix := range own {
		if prefix.Contains(ip) {
			return class
		}
	}
	return ip.String()
}

// bracketed matches a bracketed identifier in an adapter's name, with the
// space before it: "ZeroTier One [8bd5124fd6]" names a network by its id.
var bracketed = regexp.MustCompile(`\s*\[[^\]]*\]`)

// adapter_label is an adapter's name or description without the bracketed
// identifiers some drivers add to it.
func adapter_label(name string) string {
	return strings.TrimSpace(bracketed.ReplaceAllString(name, ""))
}

// adapter_kind names an adapter by its interface type, with a tunnel type
// making it a tunnel whatever it says it is.
func adapter_kind(if_type winipcfg.IfType, tunnel winipcfg.TunnelType) string {
	switch {
	case if_type == winipcfg.IfTypeSoftwareLoopback:
		return "loopback"
	case if_type == winipcfg.IfTypeTunnel, tunnel != winipcfg.TunnelTypeNone:
		return "tunnel"
	case if_type == winipcfg.IfTypeIEEE80211:
		return "wifi"
	case if_type == winipcfg.IfTypeEthernetCSMACD:
		return "ethernet"
	case if_type == winipcfg.IfTypeWwanpp, if_type == winipcfg.IfTypeWwanpp2:
		return "mobile"
	}
	return "other"
}

// known_mtu is the MTU Windows reported, or 0 where it reported none.
func known_mtu(mtu uint32) uint32 {
	if mtu == ^uint32(0) {
		return 0
	}
	return mtu
}

// is_default_prefix reports whether a route to prefix reaches everything, or
// half of everything: a VPN that takes over the default route does it as two
// /1 routes, so that its own endpoint's route stays the more specific.
func is_default_prefix(prefix netip.Prefix) bool {
	return prefix.IsValid() && prefix.Bits() <= 1
}

// Network_summary reads the adapters that are up and describes each one; see
// Adapter_summary. A route table that cannot be read is an error rather than
// every adapter claiming not to carry the default route.
func Network_summary() ([]Adapter_summary, error) {
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC, winipcfg.GAAFlagIncludeGateways)
	if err != nil {
		return nil, fmt.Errorf("network summary: reading the adapters: %w", err)
	}
	routes, err := winipcfg.GetIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("network summary: reading the routes: %w", err)
	}
	carries_default := make(map[winipcfg.LUID]bool)
	for i := range routes {
		if is_default_prefix(routes[i].DestinationPrefix.Prefix()) {
			carries_default[routes[i].InterfaceLUID] = true
		}
	}

	// The machine's own networks, for telling a resolver that is on them from
	// one that is not.
	var own []netip.Prefix
	for _, adapter := range adapters {
		if adapter.OperStatus != winipcfg.IfOperStatusUp {
			continue
		}
		for address := adapter.FirstUnicastAddress; address != nil; address = address.Next {
			if ip, ok := netip.AddrFromSlice(address.Address.IP()); ok {
				if prefix, err := ip.Unmap().Prefix(int(address.OnLinkPrefixLength)); err == nil {
					own = append(own, prefix)
				}
			}
		}
	}

	summary := []Adapter_summary{}
	for _, adapter := range adapters {
		if adapter.OperStatus != winipcfg.IfOperStatusUp {
			continue
		}
		item := Adapter_summary{
			Name:        adapter_label(adapter.FriendlyName()),
			Description: adapter_label(adapter.Description()),
			Kind:        adapter_kind(adapter.IfType, adapter.TunnelType),
			Hardware:    Is_hardware_interface(adapter.LUID),
			Mtu:         known_mtu(adapter.MTU),
			Default:     carries_default[adapter.LUID],
		}
		for address := adapter.FirstUnicastAddress; address != nil; address = address.Next {
			ip, ok := netip.AddrFromSlice(address.Address.IP())
			if !ok {
				continue
			}
			class := address_class(ip)
			switch {
			case class == "":
			case ip.Unmap().Is4():
				item.Ipv4 = append(item.Ipv4, class)
			default:
				item.Ipv6 = append(item.Ipv6, class)
			}
		}
		for gateway := adapter.FirstGatewayAddress; gateway != nil; gateway = gateway.Next {
			if ip, ok := netip.AddrFromSlice(gateway.Address.IP()); ok && !ip.IsUnspecified() {
				item.Gateway = true
			}
		}
		for server := adapter.FirstDNSServerAddress; server != nil; server = server.Next {
			if ip, ok := netip.AddrFromSlice(server.Address.IP()); ok {
				if entry := dns_entry(ip, own); entry != "" {
					item.Dns = append(item.Dns, entry)
				}
			}
		}
		summary = append(summary, item)
	}
	return summary, nil
}
