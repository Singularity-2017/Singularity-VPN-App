package vless

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

const test_uri = "vless://11111111-2222-4333-8444-555555555555@example.invalid:443?type=tcp&security=reality&encryption=none&flow=xtls-rprx-vision&sni=example.org&fp=safari&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd"

func Test_secure_uri(t *testing.T) {
	if _, err := Parse_uri([]byte(test_uri)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{test_uri + "&sni=evil.invalid", test_uri + "&allowInsecure=1", strings.Replace(test_uri, "security=reality", "security=none", 1), strings.Replace(test_uri, ":443", ":0", 1), strings.Replace(test_uri, "type=tcp", "type=ws", 1), strings.Replace(test_uri, "sni=example.org", "sni=example.org%0aevil", 1), strings.Replace(test_uri, "11111111-2222-4333-8444-555555555555", "secret", 1), test_uri + "\n"} {
		if _, err := Parse_uri([]byte(raw)); err == nil {
			t.Error("unsafe URI accepted")
		} else if strings.Contains(err.Error(), "11111111") || strings.Contains(err.Error(), "pbk=") {
			t.Error("credential disclosed")
		}
	}
}

func Test_full_tun_excludes_only_endpoint(t *testing.T) {
	for _, endpoint := range []netip.Addr{netip.MustParseAddr("203.0.113.9"), netip.MustParseAddr("2001:db8::9")} {
		routes := routes_except(endpoint)
		for _, p := range routes {
			if p.Contains(endpoint) {
				t.Fatal("endpoint routed into TUN")
			}
		}
		for _, addr := range []string{"0.0.0.0", "1.1.1.1", "203.0.113.8", "203.0.113.10", "255.255.255.255", "::", "2001:db8::8", "2001:db8::a", "ffff::1"} {
			a := netip.MustParseAddr(addr)
			count := 0
			for _, p := range routes {
				if p.Contains(a) {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("coverage %s: %d", addr, count)
			}
		}
		c, _ := Parse_uri([]byte(test_uri))
		data, err := c.native_config(endpoint, "test-tun")
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if json.Unmarshal(data, &cfg) != nil {
			t.Fatal("invalid JSON")
		}
		if strings.Contains(string(data), "example.invalid") {
			t.Fatal("endpoint was not pinned before routing")
		}
	}
}

func Test_native_tunnel_uses_IPv4_DNS_and_contains_IPv6(t *testing.T) {
	u, err := Parse_uri([]byte(test_uri))
	if err != nil {
		t.Fatal(err)
	}
	data, err := u.native_config(netip.MustParseAddr("203.0.113.9"), "test-tun")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DNS struct {
			Query_strategy string `json:"queryStrategy"`
		} `json:"dns"`
		Outbounds []struct{ Tag, Protocol string } `json:"outbounds"`
		Routing   struct {
			Rules []struct {
				IP       []string `json:"ip"`
				Outbound string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	// TUN accepts IPv6 TCP locally even when the node cannot reach IPv6.
	// Suppress AAAA selection and keep literal/cached IPv6 inside the block route.
	if cfg.DNS.Query_strategy != "UseIPv4" {
		t.Fatal("AAAA answers can select an unusable IPv6 path")
	}
	blocked := ""
	for _, out := range cfg.Outbounds {
		if out.Protocol == "blackhole" {
			blocked = out.Tag
		}
	}
	if blocked == "" {
		t.Fatal("no IPv6 block outbound")
	}
	for _, rule := range cfg.Routing.Rules {
		if rule.Outbound == blocked && len(rule.IP) == 1 && rule.IP[0] == "::/0" {
			return
		}
	}
	t.Fatal("literal or cached IPv6 can bypass the block policy")
}
