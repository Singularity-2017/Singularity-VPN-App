package connection

import (
	"context"
	"strings"
	"testing"

	"singularity-vpn-app/internal/api"
)

// On a network where plain WireGuard is throttled the control plane
// recommends a protocol this build has no adapter for. Its alternatives on
// the same node include one this build speaks: connect with that rather
// than fail with "no adapter for this protocol".
func Test_a_recommended_protocol_this_build_lacks_falls_back_to_an_offered_one_it_has(t *testing.T) {
	env := new_test_env(t)
	env.control_plane.recommend_result = api.Recommendation{
		Node_id:  "de-01",
		Protocol: "amneziawg",
		Alternatives: []api.Alternative{
			{Node: "de-01", Protocol: "openvpn", State: "works"},
			{Node: "de-01", Protocol: "wireguard", State: "unknown"},
		},
	}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	for _, asked := range env.control_plane.asked_protocols {
		if asked != "wireguard" {
			t.Fatalf("asked the control plane for %q (all: %v); want only wireguard, the offered protocol this build has", asked, env.control_plane.asked_protocols)
		}
	}
	if len(env.control_plane.asked_protocols) == 0 || env.store.state.Last_protocol != "wireguard" {
		t.Fatalf("asked %v, connected with %q; want wireguard", env.control_plane.asked_protocols, env.store.state.Last_protocol)
	}
}

// Nothing offered that this build speaks: the failure must say so plainly.
func Test_no_offered_protocol_this_build_has_is_refused_by_name(t *testing.T) {
	env := new_test_env(t)
	env.control_plane.recommend_result = api.Recommendation{
		Node_id:      "de-01",
		Protocol:     "amneziawg",
		Alternatives: []api.Alternative{{Node: "de-01", Protocol: "openvpn"}},
	}
	manager := env.build()
	err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto})
	if err == nil {
		t.Fatal("Connect succeeded with no protocol this build has")
	}
	if len(env.control_plane.asked_protocols) != 0 {
		t.Fatal("a profile was provisioned for a protocol this build cannot use")
	}
}

// The control plane has seen WireGuard freeze on this network: bringing it
// up only ends in "the tunnel is up but nothing is reachable". Say why
// instead, and provision nothing.
func Test_a_frozen_alternative_is_refused_with_the_reason(t *testing.T) {
	env := new_test_env(t)
	env.control_plane.recommend_result = api.Recommendation{
		Node_id:  "de-01",
		Protocol: "amneziawg",
		Alternatives: []api.Alternative{
			{Node: "de-01", Protocol: "wireguard", State: "frozen"},
			{Node: "de-01", Protocol: "openvpn", State: "unknown"},
		},
	}
	manager := env.build()
	err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto})
	if err == nil || !strings.Contains(err.Error(), "freeze") {
		t.Fatalf("Connect = %v; want a refusal saying wireguard freezes here", err)
	}
	if len(env.control_plane.asked_protocols) != 0 || env.protocol.connect_calls != 0 {
		t.Fatal("a tunnel was provisioned or brought up for a protocol known to freeze")
	}
}
