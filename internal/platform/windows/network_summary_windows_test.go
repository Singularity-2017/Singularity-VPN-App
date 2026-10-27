//go:build windows

package windows

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func Test_address_class(t *testing.T) {
	cases := []struct{ address, want string }{
		{"192.168.1.20", class_private},
		{"10.0.0.5", class_private},
		{"172.16.0.1", class_private},
		{"172.31.255.255", class_private},
		{"172.32.0.1", class_public},
		{"100.64.0.1", class_cgnat},
		{"100.127.255.254", class_cgnat},
		{"100.128.0.1", class_public},
		{"100.63.255.255", class_public},
		{"169.254.10.20", class_link_local},
		{"8.8.8.8", class_public},
		{"203.0.113.9", class_public},
		{"127.0.0.1", class_loopback},
		{"0.0.0.0", ""},
		{"224.0.0.251", ""},
		{"fd12:3456:789a::1", class_ula},
		{"fec0:0:0:ffff::1", class_site_local},
		{"fc00::1", class_ula},
		{"fe80::1", class_link_local},
		{"2001:db8::1", class_global},
		{"2a00:1450:4001::200e", class_global},
		{"::1", class_loopback},
		{"::", ""},
		{"ff02::1", ""},
		{"::ffff:192.168.1.1", class_private}, // an IPv4 address in IPv6's clothes
	}
	for _, tc := range cases {
		if got := address_class(netip.MustParseAddr(tc.address)); got != tc.want {
			t.Errorf("address_class(%s) = %q, want %q", tc.address, got, tc.want)
		}
	}
	if address_class(netip.Addr{}) != "" {
		t.Error("no address has a class")
	}
}

func Test_adapter_kind(t *testing.T) {
	cases := []struct {
		name      string
		if_type   winipcfg.IfType
		tunnel    winipcfg.TunnelType
		want_kind string
	}{
		{"wi-fi", winipcfg.IfTypeIEEE80211, winipcfg.TunnelTypeNone, "wifi"},
		{"ethernet", winipcfg.IfTypeEthernetCSMACD, winipcfg.TunnelTypeNone, "ethernet"},
		{"mobile gsm", winipcfg.IfTypeWwanpp, winipcfg.TunnelTypeNone, "mobile"},
		{"mobile cdma", winipcfg.IfTypeWwanpp2, winipcfg.TunnelTypeNone, "mobile"},
		{"loopback", winipcfg.IfTypeSoftwareLoopback, winipcfg.TunnelTypeNone, "loopback"},
		{"a tunnel by its type", winipcfg.IfTypeTunnel, winipcfg.TunnelTypeNone, "tunnel"},
		{"a tunnel by its tunnel type", winipcfg.IfTypeEthernetCSMACD, winipcfg.TunnelTypeTeredo, "tunnel"},
		{"anything else", winipcfg.IfTypeOther, winipcfg.TunnelTypeNone, "other"},
	}
	for _, tc := range cases {
		if got := adapter_kind(tc.if_type, tc.tunnel); got != tc.want_kind {
			t.Errorf("%s: adapter_kind = %q, want %q", tc.name, got, tc.want_kind)
		}
	}
}

func Test_dns_entry_keeps_only_a_public_resolver_outside_the_machines_networks(t *testing.T) {
	own := []netip.Prefix{
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"), // a machine with a public address, and its provider's resolver beside it
		netip.MustParsePrefix("2001:db8:1::/64"),
	}
	cases := []struct{ server, want string }{
		{"8.8.8.8", "8.8.8.8"},
		{"1.1.1.1", "1.1.1.1"},
		{"2001:4860:4860::8888", "2001:4860:4860::8888"},
		{"::ffff:9.9.9.9", "9.9.9.9"},
		{"192.168.1.1", class_private},
		{"10.0.0.1", class_private},
		{"100.64.0.1", class_cgnat},
		{"169.254.1.1", class_link_local},
		{"127.0.0.1", class_loopback},
		{"203.0.113.1", class_public}, // inside the machine's own /24: the class, not the address
		{"2001:db8:1::53", class_global},
		{"2001:db8:2::53", "2001:db8:2::53"},
		{"fd12::1", class_ula},
		{"fe80::1", class_link_local},
		{"fec0:0:0:ffff::1", class_site_local},
		{"0.0.0.0", ""},
		{"::", ""},
	}
	for _, tc := range cases {
		if got := dns_entry(netip.MustParseAddr(tc.server), own); got != tc.want {
			t.Errorf("dns_entry(%s) = %q, want %q", tc.server, got, tc.want)
		}
	}
	if got := dns_entry(netip.MustParseAddr("203.0.113.1"), nil); got != "203.0.113.1" {
		t.Errorf("with no networks of its own, a public resolver is %q", got)
	}
}

func Test_adapter_label_drops_bracketed_identifiers(t *testing.T) {
	cases := map[string]string{
		"ZeroTier One [8bd5124fd6]":           "ZeroTier One",
		"ZeroTier One [8bd5124fd6] [x]":       "ZeroTier One",
		"Wi-Fi":                               "Wi-Fi",
		"Ethernet 2":                          "Ethernet 2",
		"vEthernet (WSL (Hyper-V firewall))":  "vEthernet (WSL (Hyper-V firewall))",
		"Local Area Connection* 10 [abc] foo": "Local Area Connection* 10 foo",
		"[abc]":                               "",
		"":                                    "",
	}
	for in, want := range cases {
		if got := adapter_label(in); got != want {
			t.Errorf("adapter_label(%q) = %q, want %q", in, got, want)
		}
	}
}

func Test_known_mtu_is_absent_when_windows_has_none(t *testing.T) {
	if known_mtu(^uint32(0)) != 0 || known_mtu(1500) != 1500 || known_mtu(0) != 0 {
		t.Fatal("an MTU that is not a size was passed on")
	}
}

func Test_is_default_prefix(t *testing.T) {
	cases := []struct {
		prefix string
		want   bool
	}{
		{"0.0.0.0/0", true},
		{"::/0", true},
		{"0.0.0.0/1", true},
		{"128.0.0.0/1", true},
		{"::/1", true},
		{"10.0.0.0/8", false},
		{"192.168.1.0/24", false},
		{"192.168.1.1/32", false},
		{"fe80::/64", false},
	}
	for _, tc := range cases {
		if got := is_default_prefix(netip.MustParsePrefix(tc.prefix)); got != tc.want {
			t.Errorf("is_default_prefix(%s) = %v, want %v", tc.prefix, got, tc.want)
		}
	}
	if is_default_prefix(netip.Prefix{}) {
		t.Error("no route is a default route")
	}
}

// The summary is read from this machine, so only what it must never hold is
// checked: an address of ours in the address fields, or a hardware address.
func Test_network_summary_holds_classes_and_no_machine_address(t *testing.T) {
	summary, err := Network_summary()
	if err != nil {
		t.Fatalf("Network_summary: %v", err)
	}
	classes := map[string]bool{
		class_private: true, class_cgnat: true, class_link_local: true, class_public: true,
		class_loopback: true, class_ula: true, class_site_local: true, class_global: true,
	}
	for _, adapter := range summary {
		for _, class := range append(append([]string{}, adapter.Ipv4...), adapter.Ipv6...) {
			if !classes[class] {
				t.Errorf("%s: %q is not an address class", adapter.Name, class)
			}
		}
		for _, server := range adapter.Dns {
			// A class, or the address of a public resolver: never a private one.
			if classes[server] {
				continue
			}
			ip, err := netip.ParseAddr(server)
			if err != nil || (address_class(ip) != class_public && address_class(ip) != class_global) {
				t.Errorf("%s: DNS entry %q is neither a class nor a public address", adapter.Name, server)
			}
		}
		if strings.Contains(adapter.Name, "[") || strings.Contains(adapter.Description, "[") {
			t.Errorf("%q / %q carries a bracketed identifier", adapter.Name, adapter.Description)
		}
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"mac"`, `"physical`, `"ssid"`} {
		if strings.Contains(strings.ToLower(string(raw)), field) {
			t.Errorf("the summary has a %s field", field)
		}
	}
}
