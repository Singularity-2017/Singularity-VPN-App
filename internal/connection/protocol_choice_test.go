package connection

import (
	"context"
	"errors"
	"singularity-vpn-app/internal/recovery"
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

// Of the offered protocols, the one that carried traffic on this node last
// time is tried first, whatever order the control plane listed them in.
func Test_the_protocol_that_worked_last_on_the_node_is_tried_first(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	env.store.state.Client_id = "client-1"
	env.store.state.Last_node_id = "de-01"
	env.store.state.Last_protocol = "amneziawg"
	env.control_plane.recommend_result = api.Recommendation{
		Node_id:  "de-01",
		Protocol: "openvpn",
		Alternatives: []api.Alternative{
			{Node: "de-01", Protocol: "wireguard", State: "unknown"},
			{Node: "de-01", Protocol: "amneziawg", State: "unknown"},
		},
	}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if env.protocol.connect_calls != 0 {
		t.Fatalf("wireguard was brought up %d times; amneziawg worked last time on this node", env.protocol.connect_calls)
	}
	if env.store.state.Last_protocol != "amneziawg" {
		t.Fatalf("connected with %q; want amneziawg", env.store.state.Last_protocol)
	}
}

// What worked on another node says nothing about this one.
func Test_the_last_protocol_of_another_node_does_not_override_obfuscated_default(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	env.store.state.Client_id = "client-1"
	env.store.state.Last_node_id = "nl-01"
	env.store.state.Last_protocol = "amneziawg"
	env.control_plane.recommend_result = api.Recommendation{
		Node_id:  "de-01",
		Protocol: "openvpn",
		Alternatives: []api.Alternative{
			{Node: "de-01", Protocol: "wireguard", State: "unknown"},
			{Node: "de-01", Protocol: "amneziawg", State: "unknown"},
		},
	}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if env.store.state.Last_protocol != "amneziawg" {
		t.Fatalf("connected with %q; want offered amneziawg on this node", env.store.state.Last_protocol)
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

// A goal change asks again; the control plane still recommends a protocol
// this build lacks. The tunnel is up on an offered alternative -- the one
// recovery reached after the first guess froze -- so it stays, instead of
// being torn down for the first alternative in the list.
func Test_a_goal_change_keeps_a_tunnel_up_on_an_offered_alternative(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	amnezia := &fake_protocol{name: "amneziawg"}
	env.protocols.by_name["amneziawg"] = amnezia
	offer := api.Recommendation{
		Node_id:  "de-01",
		Protocol: "openvpn",
		Alternatives: []api.Alternative{
			{Node: "de-01", Protocol: "wireguard", State: "unknown"},
			{Node: "de-01", Protocol: "amneziawg", State: "unknown"},
		},
	}
	env.control_plane.recommend_result = offer
	wireguard := env.protocols.by_name["wireguard"]
	wireguard.connect_err = errors.New("verification: nothing is reachable")
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if manager.current.Protocol != "amneziawg" {
		t.Fatalf("connected with %q; the test needs amneziawg", manager.current.Protocol)
	}
	calls := amnezia.connect_calls
	wireguard.connect_err = nil // a switch to it would now succeed, and show
	if err := manager.Apply_strategy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.current.Protocol != "amneziawg" || amnezia.connect_calls != calls || amnezia.disconnect_calls != 0 {
		t.Fatalf("now on %q, amneziawg connects %d -> %d, disconnects %d; want the tunnel left alone",
			manager.current.Protocol, calls, amnezia.connect_calls, amnezia.disconnect_calls)
	}
}

func Test_automatic_protocol_policy_respects_offers_and_local_failures(t *testing.T) {
	for _, test := range []struct {
		name                       string
		state                      string
		supported, cooling, pinned bool
		want                       string
	}{
		{"old recommendation", "unknown", true, false, false, "amneziawg"},
		{"frozen obfuscation", "frozen", true, false, false, "wireguard"},
		{"unsupported obfuscation", "unknown", false, false, false, "wireguard"},
		{"cooling obfuscation", "unknown", true, true, false, "wireguard"},
		{"pinned node", "unknown", true, false, true, "amneziawg"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := new_test_env(t)
			if test.supported {
				env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
			}
			env.store.state.Last_node_id = "de-01"
			env.store.state.Last_protocol = "wireguard"
			if test.pinned {
				env.store.state.Strategy.Node_id = "de-01"
			}
			m := env.build()
			if test.cooling {
				m.policy.Record_failure(m.now(), recovery.Target{Node_id: "de-01", Protocol: "amneziawg"})
			}
			rec := api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Alternatives: []api.Alternative{{Node: "de-01", Protocol: "amneziawg", State: test.state}, {Node: "nl-01", Protocol: "amneziawg", State: "works"}}}
			target, fault := m.spoken_target(rec)
			if fault != nil || target.Node_id != "de-01" || target.Protocol != test.want {
				t.Fatalf("target=%+v fault=%v", target, fault)
			}
		})
	}
}

func Test_offered_current_protocol_stays_when_recommendation_is_supported(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	awg := &fake_protocol{name: "amneziawg"}
	env.protocols.by_name["amneziawg"] = awg
	env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Alternatives: []api.Alternative{{Node: "de-01", Protocol: "amneziawg", State: "unknown"}}}
	m := env.build()
	if err := m.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatal(err)
	}
	calls := awg.connect_calls
	if err := m.Apply_strategy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.current.Protocol != "amneziawg" || awg.connect_calls != calls || awg.disconnect_calls != 0 {
		t.Fatal("supported recommendation churned working obfuscated tunnel")
	}
}

func Test_manual_plain_protocol_remains_authoritative(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	m := env.build()
	if err := m.Connect(context.Background(), Connect_request{Mode: Mode_manual, Node_id: "de-01", Protocol: "wireguard"}); err != nil {
		t.Fatal(err)
	}
	if m.current.Protocol != "wireguard" {
		t.Fatalf("manual choice changed: %+v", m.current)
	}
}

func Test_recovery_offers_prefer_obfuscated_within_same_node(t *testing.T) {
	got := alternatives_from([]api.Alternative{{Node: "de", Protocol: "wireguard"}, {Node: "nl", Protocol: "openvpn"}, {Node: "de", Protocol: "amneziawg"}, {Node: "de", Protocol: "openvpn", State: "frozen"}})
	if len(got) != 3 || got[0].Protocol != "amneziawg" || got[1].Node_id != "nl" || got[2].Protocol != "wireguard" {
		t.Fatalf("offers=%+v", got)
	}
}

func Test_policy_preference_does_not_hot_switch_existing_plain_connection(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "wireguard"}
	m := env.build()
	if err := m.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatal(err)
	}
	calls := env.protocol.connect_calls
	env.control_plane.recommend_result.Alternatives = []api.Alternative{{Node: "de-01", Protocol: "amneziawg", State: "unknown"}}
	if err := m.Apply_strategy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.current.Protocol != "wireguard" || env.protocol.connect_calls != calls || env.protocol.disconnect_calls != 0 {
		t.Fatal("policy-only update switched working tunnel")
	}
}

func Test_failed_preferred_alternative_recovers_to_offered_primary(t *testing.T) {
	for _, fresh_profile := range []bool{false, true} {
		env := new_test_env(t)
		if fresh_profile {
			env.control_plane.create_profile_result.Protocol = "amneziawg"
		} else {
			env.store.state.Client_id = "client-1"
		}
		awg := &fake_protocol{name: "amneziawg", connect_err: errors.New("network timeout")}
		env.protocols.by_name["amneziawg"] = awg
		env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Alternatives: []api.Alternative{{Node: "de-01", Protocol: "amneziawg", State: "unknown"}}}
		m := env.build()
		if err := m.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
			t.Fatal(err)
		}
		if awg.connect_calls != 1 || m.current.Protocol != "wireguard" || env.protocol.connect_calls != 1 {
			t.Fatalf("AWG attempts=%d WG attempts=%d target=%+v", awg.connect_calls, env.protocol.connect_calls, m.current)
		}
	}
}

func Test_recovery_primary_union_excludes_frozen_duplicate_and_chosen(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	m := env.build()
	chosen := recovery.Target{Node_id: "de-01", Protocol: "amneziawg"}
	rec := api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Alternatives: []api.Alternative{{Node: "de-01", Protocol: "wireguard", State: "frozen"}, {Node: "de-01", Protocol: "amneziawg", State: "unknown"}}}
	if got := m.recovery_offers(rec, chosen); len(got) != 0 {
		t.Fatalf("frozen primary resurrected: %+v", got)
	}
	rec.Alternatives[0].State = "unknown"
	got := m.recovery_offers(rec, chosen)
	if len(got) != 1 || got[0].Protocol != "wireguard" {
		t.Fatalf("offers=%+v", got)
	}
}
