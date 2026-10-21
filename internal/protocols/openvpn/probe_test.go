package openvpn

import (
	"context"
	"testing"
)

func Test_a_split_tunnel_is_probed_through_the_resolver_the_node_pushed(t *testing.T) {
	h := new_harness(t, new_fake_process(
		"PUSH: Received control message: 'PUSH_REPLY,route 10.224.0.0 255.224.0.0,route 198.51.100.7,push-continuation 2'",
		"PUSH: Received control message: 'PUSH_REPLY,dhcp-option DNS 10.29.0.1,route-gateway 10.29.0.1,topology subnet,ifconfig 10.29.0.2 255.255.248.0'",
		"Initialization Sequence Completed"))
	if got := h.adapter.Probe_target(); got != "" {
		t.Fatalf("Probe_target() before connecting = %q, want none", got)
	}
	if err := h.connect(context.Background()); err != nil {
		t.Fatalf("Connect = %v", err)
	}
	if got := h.adapter.Probe_target(); got != "10.29.0.1:53" {
		t.Fatalf("Probe_target() = %q, want the pushed resolver on port 53", got)
	}
	h.adapter.Disconnect(context.Background())
	if got := h.adapter.Probe_target(); got != "" {
		t.Fatalf("Probe_target() after disconnecting = %q, want none", got)
	}
}

func Test_what_the_tunnel_carries_decides_the_probe(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		push    string
		want    string
	}{
		{"the whole machine, pushed", "", "PUSH_REPLY,redirect-gateway def1 bypass-dhcp,dhcp-option DNS 10.8.0.1,ifconfig 10.8.0.2 255.255.255.0", ""},
		{"the whole machine, in the profile", "redirect-gateway def1\n", "PUSH_REPLY,dhcp-option DNS 10.8.0.1,ifconfig 10.8.0.2 255.255.255.0", ""},
		{"a resolver in the tunnel's subnet", "", "PUSH_REPLY,dhcp-option DNS 10.8.0.1,ifconfig 10.8.0.2 255.255.255.0", "10.8.0.1:53"},
		{"a resolver a profile route carries", "route 10.30.0.0 255.255.0.0\n", "PUSH_REPLY,dhcp-option DNS 10.30.0.53", "10.30.0.53:53"},
		{"a resolver outside the tunnel", "", "PUSH_REPLY,dhcp-option DNS 1.1.1.1,ifconfig 10.8.0.2 255.255.255.0", ""},
		{"a resolver routed around the tunnel", "", "PUSH_REPLY,route 10.30.0.0 255.255.0.0 vpn_gateway,route 10.30.0.0 255.255.255.0 net_gateway,dhcp-option DNS 10.30.0.53", ""},
		{"a resolver past a hole routed around", "", "PUSH_REPLY,route 10.30.0.0 255.255.0.0,route 10.30.1.0 255.255.255.0 net_gateway,dhcp-option DNS 10.30.0.53", "10.30.0.53:53"},
		{"net30: the second address is the peer", "", "PUSH_REPLY,dhcp-option DNS 10.8.0.1,ifconfig 10.8.0.6 10.8.0.5", ""},
		{"no resolver pushed", "", "PUSH_REPLY,route 10.0.0.0 255.0.0.0", ""},
	}
	for _, c := range cases {
		var got carried
		got.read_profile([]byte(c.profile))
		got.read_push("PUSH: Received control message: '" + c.push + "'")
		if target := got.probe_target(); target != c.want {
			t.Errorf("%s: probe target = %q, want %q", c.name, target, c.want)
		}
	}
}
