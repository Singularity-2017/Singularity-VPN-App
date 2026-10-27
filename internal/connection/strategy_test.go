package connection

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/protocols/openvpn"
	"singularity-vpn-app/internal/protocols/wireguard"
	"singularity-vpn-app/internal/recovery"
	"singularity-vpn-app/internal/state"
)

func Test_the_stored_strategy_and_this_connection_go_into_the_recommendation(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.store.state.Strategy = state.Strategy{Anonymity: "high", Country: "DE", Route: true}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	want := api.Recommend_request{Anonymity: "high", Country: "DE", Route: true, Client_id: "client-1"}
	if len(env.control_plane.recommend_requests) != 1 || env.control_plane.recommend_requests[0] != want {
		t.Fatalf("recommend asked with %+v, want %+v", env.control_plane.recommend_requests, want)
	}
}

// The server's own pick at provisioning knows nothing of a country or a
// server picked by hand, so a first connect with either asks first.
func Test_a_first_connect_for_a_country_provisions_on_the_recommended_node(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Strategy = state.Strategy{Country: "NL"}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "nl-02", Protocol: "wireguard"}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.control_plane.recommend_calls != 1 || len(env.control_plane.create_profile_nodes) != 1 || env.control_plane.create_profile_nodes[0] != "nl-02" {
		t.Fatalf("recommend calls %d, provisioned on %v; want one recommendation and nl-02", env.control_plane.recommend_calls, env.control_plane.create_profile_nodes)
	}
}

// Provisioning does not know what this build speaks, so even with no goal
// the recommendation is asked first and its node is provisioned on.
func Test_a_first_connect_with_no_goals_provisions_on_the_recommendation(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.control_plane.recommend_calls != 1 || env.control_plane.create_profile_nodes[0] != "de-01" {
		t.Fatalf("recommend calls %d, provisioned on %v; want the recommended node", env.control_plane.recommend_calls, env.control_plane.create_profile_nodes)
	}
}

func Test_a_first_connect_with_any_goal_asks_first(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Strategy = state.Strategy{Bypass: true}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.control_plane.recommend_calls != 1 || env.control_plane.create_profile_nodes[0] != "de-01" {
		t.Fatalf("recommend calls %d, provisioned on %v; want the recommendation first", env.control_plane.recommend_calls, env.control_plane.create_profile_nodes)
	}
}

// A goal touched while connected must not leave the person without a
// tunnel when the new server never comes up.
func Test_a_new_strategy_whose_server_fails_goes_back_to_the_old_one(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "nl-02", Protocol: "wireguard"}
	calls := env.control_plane.fetch_config_calls
	broken := errors.New("nl-02 cannot mint a config")
	env.control_plane.fetch_config_sequence = make([]fetch_config_response, calls)
	for i := range env.control_plane.fetch_config_sequence {
		env.control_plane.fetch_config_sequence[i] = fetch_config_response{config: env.control_plane.fetch_config_result}
	}
	env.control_plane.fetch_config_sequence = append(env.control_plane.fetch_config_sequence, fetch_config_response{err: broken})
	if err := manager.Apply_strategy(context.Background()); err == nil {
		t.Fatal("Apply_strategy() reported success for a server that never came up")
	}
	session, _ := manager.Session()
	if manager.State() != State_connected || session.Node_id != "de-01" {
		t.Fatalf("after a failed move: state %v on %q; want connected again on de-01", manager.State(), session.Node_id)
	}
}

func Test_a_server_picked_by_hand_keeps_recovery_on_that_server(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.store.state.Strategy = state.Strategy{Node_id: "de-01"}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Alternatives: []api.Alternative{
		{Node: "de-01", Protocol: "openvpn"}, {Node: "nl-02", Protocol: "wireguard"},
	}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	for _, alt := range manager.alternatives {
		if alt.Node_id != "de-01" {
			t.Fatalf("alternatives %+v include another server while one is picked by hand", manager.alternatives)
		}
	}
}

// A control plane older than node_id answers for its own pick. The fault
// must say that, not that the protocols are unknown to this build.
func Test_a_control_plane_that_ignores_the_picked_server_is_named_as_the_cause(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.store.state.Strategy = state.Strategy{Node_id: "de-01"}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "nl-02", Protocol: "openvpn", Alternatives: []api.Alternative{
		{Node: "nl-02", Protocol: "wireguard"},
	}}
	manager := env.build()
	err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto})
	if err == nil || !strings.Contains(err.Error(), "answered with nl-02 instead") {
		t.Fatalf("Connect() error = %v; want the control plane named as ignoring the picked server", err)
	}
	if env.control_plane.create_profile_calls != 0 || env.control_plane.fetch_config_calls != 0 {
		t.Fatal("a connection was set up on a server nobody picked")
	}
}

func Test_a_turn_back_to_automatic_on_the_same_node_updates_why_it_was_chosen(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.store.state.Strategy = state.Strategy{Node_id: "de-01"}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Reason: "chosen_node"}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	manager.Set_strategy(state.Strategy{})
	env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Reason: "least_loaded"}
	if err := manager.Apply_strategy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if session, _ := manager.Session(); session.Reason != "least_loaded" {
		t.Fatalf("session reason %q after turning automatic back on; want least_loaded", session.Reason)
	}
}

func Test_a_session_carries_how_the_control_plane_chose_its_node(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	goals := []api.Goal_outcome{{Goal: "geo", Applied: true}, {Goal: "route", Why: "not_enough_measurements"}}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Reason: "least_loaded", Goals: goals}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	session, _ := manager.Session()
	if session.Reason != "least_loaded" || !reflect.DeepEqual(session.Goals, goals) {
		t.Fatalf("session reason %q goals %+v; want the recommendation's", session.Reason, session.Goals)
	}
}

func Test_a_new_strategy_moves_a_connection_that_is_up(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := manager.Set_strategy(state.Strategy{Country: "NL"}); err != nil {
		t.Fatal(err)
	}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "nl-02", Protocol: "wireguard"}
	if err := manager.Apply_strategy(context.Background()); err != nil {
		t.Fatalf("Apply_strategy() error = %v", err)
	}
	session, _ := manager.Session()
	if manager.State() != State_connected || session.Node_id != "nl-02" {
		t.Fatalf("after a new strategy: state %v on %q; want connected on nl-02", manager.State(), session.Node_id)
	}
	last := env.control_plane.recommend_requests[len(env.control_plane.recommend_requests)-1]
	if last.Country != "NL" {
		t.Fatalf("the recommendation was asked with %+v, want the new country", last)
	}
	if got, _ := manager.Strategy(); got.Country != "NL" || env.store.state.Strategy.Country != "NL" {
		t.Fatalf("Strategy() = %+v, stored %+v; want NL kept", got, env.store.state.Strategy)
	}
}

func Test_a_new_strategy_that_points_where_the_connection_is_changes_nothing(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	before := env.control_plane.fetch_config_calls
	if err := manager.Apply_strategy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if env.control_plane.fetch_config_calls != before {
		t.Fatal("the same target was switched to again")
	}
}

func Test_a_new_strategy_while_disconnected_waits_for_the_next_connect(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Apply_strategy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if env.control_plane.recommend_calls != 0 {
		t.Fatal("a disconnected manager asked the control plane on a strategy change")
	}
}

// A recommendation from an earlier connect says how that choice was made,
// not how a later manual one to the same node was.
func Test_a_manual_connect_does_not_borrow_an_earlier_recommendation(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Reason: "lowest_rtt"}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := manager.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_manual, Node_id: "de-01", Protocol: "wireguard"}); err != nil {
		t.Fatalf("manual Connect() error = %v", err)
	}
	if session, _ := manager.Session(); session.Reason != "" || session.Goals != nil {
		t.Fatalf("manual session carries reason %q goals %+v from an earlier recommendation", session.Reason, session.Goals)
	}
}

// A region boosted toward is asked for under the key the control plane
// measures it by; a region that is not the selected game's is refused.
func Test_the_game_region_goes_into_the_recommendation(t *testing.T) {
	for region, want := range map[string]string{"": "pubg", "eu": "pubg", "ru": ""} {
		env := new_test_env(t)
		manager := env.build()
		if err := manager.Set_games([]string{"pubg"}, region); err != nil {
			t.Fatalf("Set_games(pubg, %q) error = %v", region, err)
		}
		if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		if got := env.control_plane.recommend_requests[0].Game; got != want {
			t.Errorf("region %q asked for game %q, want %q", region, got, want)
		}
		if _, got, _ := manager.Games(); got != region {
			t.Errorf("Games() region = %q, want %q", got, region)
		}
	}
	manager := new_test_env(t).build()
	for _, bad := range [][]string{{"pubg", "cs2"}, {"cs2"}, {}} {
		if err := manager.Set_games(bad, "eu"); err == nil {
			t.Errorf("Set_games(%v, eu) accepted a region the selection does not have", bad)
		}
	}
	if err := manager.Set_games([]string{"pubg"}, "mars"); err == nil {
		t.Error("Set_games(pubg, mars) accepted an unknown region")
	}
}

func Test_a_region_played_from_home_keeps_the_game_out_of_the_tunnel(t *testing.T) {
	ru := state.State{Games: []string{"pubg"}, Game_region: "ru", Tunnel_mode: Tunnel_antizapret}
	if split := split_for(ru); len(split.Games) != 0 {
		t.Fatalf("RU split = %+v, want the game left out of the tunnel", split)
	}
	if mode := effective_mode(ru); mode != Tunnel_antizapret {
		t.Fatalf("RU mode = %s, want the person's own AntiZapret", mode)
	}
	eu := state.State{Games: []string{"pubg"}, Game_region: "eu", Tunnel_mode: Tunnel_antizapret}
	if split := split_for(eu); len(split.Games) == 0 {
		t.Fatal("EU split carries no game; the boost toward EU goes through the tunnel")
	}
}

// A protocol picked by hand is the one connected with, on whichever server
// offers it, and recovery stays on it: another protocol is not what was
// chosen.
func Test_a_protocol_picked_by_hand_is_used_and_kept_through_recovery(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.store.state.Strategy = state.Strategy{Protocol: "openvpn"}
	env.protocols.by_name["openvpn"] = &fake_protocol{name: "openvpn"}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "wireguard", Alternatives: []api.Alternative{
		{Node: "de-01", Protocol: "openvpn"}, {Node: "nl-02", Protocol: "wireguard"}, {Node: "nl-02", Protocol: "openvpn"},
	}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if got, _ := manager.Session(); got.Protocol != "openvpn" || got.Node_id != "de-01" {
		t.Fatalf("connected with %s on %s; want openvpn on de-01", got.Protocol, got.Node_id)
	}
	if len(manager.alternatives) == 0 {
		t.Fatal("no alternatives kept; want openvpn on nl-02")
	}
	for _, alt := range manager.alternatives {
		if alt.Protocol != "openvpn" {
			t.Fatalf("alternatives %+v include another protocol while one is picked by hand", manager.alternatives)
		}
	}
}

// A protocol picked by hand that nothing offers is named as the reason,
// rather than the build or the network.
func Test_a_protocol_picked_by_hand_that_is_not_offered_is_named_as_the_cause(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	manager.local_state.Strategy = state.Strategy{Protocol: "openvpn"}
	_, fault := manager.spoken_target(api.Recommendation{Node_id: "de-01", Protocol: "wireguard"})
	if fault == nil || fault.Kind != Fault_protocol_unavailable || !strings.Contains(fault.Error(), "picked by hand, openvpn") {
		t.Fatalf("fault = %v; want protocol_unavailable naming the protocol picked by hand", fault)
	}
}

// effective_mode cannot ask an adapter, so it keeps the name of the one
// protocol that carries only the whole machine; this holds the name to the
// adapters.
func Test_the_whole_machine_protocol_is_the_one_whose_adapter_says_so(t *testing.T) {
	var ovpn any = &openvpn.Adapter{}
	var wg any = &wireguard.Adapter{}
	if _, whole := ovpn.(protocols.Whole_machine); !whole || !carries_only_whole_machine(openvpn.Name) {
		t.Errorf("openvpn: adapter whole machine %v, named whole machine %v", whole, carries_only_whole_machine(openvpn.Name))
	}
	if _, whole := wg.(protocols.Whole_machine); whole || carries_only_whole_machine("wireguard") || carries_only_whole_machine("amneziawg") {
		t.Error("wireguard and amneziawg carry games alone")
	}
}

// A server and protocol this network sent a problem report about steps
// aside for another usable offer on the same server, and is tried last in
// recovery; alone, it is still used.
func Test_what_this_network_reported_problems_with_steps_aside(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	manager := env.build()
	report := []api.Connection_evidence{{Source: "app", Node_id: "de-01", Protocol: "amneziawg", Mode: "full", Failures: 1, Reported: 1}}
	rec := api.Recommendation{Node_id: "de-01", Protocol: "amneziawg", Evidence: report,
		Alternatives: []api.Alternative{{Node: "de-01", Protocol: "wireguard"}}}
	target, fault := manager.spoken_target(rec)
	if fault != nil || target != (recovery.Target{Node_id: "de-01", Protocol: "wireguard"}) {
		t.Fatalf("target=%+v fault=%v; want wireguard on de-01", target, fault)
	}
	offers := manager.recovery_offers(rec, target)
	if len(offers) != 1 || offers[0].Protocol != "amneziawg" {
		t.Fatalf("recovery offers %+v; want the reported amneziawg kept as the last resort", offers)
	}
	alone := api.Recommendation{Node_id: "de-01", Protocol: "amneziawg", Evidence: report}
	if target, fault := manager.spoken_target(alone); fault != nil || target.Protocol != "amneziawg" {
		t.Fatalf("alone: target=%+v fault=%v; want amneziawg still used", target, fault)
	}
}

// A report made in another mode does not move this one.
func Test_a_report_from_another_mode_moves_nothing(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	manager := env.build()
	rec := api.Recommendation{Node_id: "de-01", Protocol: "amneziawg",
		Evidence:     []api.Connection_evidence{{Source: "app", Node_id: "de-01", Protocol: "amneziawg", Mode: "games", Failures: 1, Reported: 1}},
		Alternatives: []api.Alternative{{Node: "de-01", Protocol: "wireguard"}}}
	if target, fault := manager.spoken_target(rec); fault != nil || target.Protocol != "amneziawg" {
		t.Fatalf("target=%+v fault=%v; want amneziawg, the report was about games alone", target, fault)
	}
}

// A protocol picked by hand that freezes on this network is named as the
// cause, not the build.
func Test_a_picked_protocol_that_freezes_is_named_not_the_build(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	manager.local_state.Strategy = state.Strategy{Protocol: "wireguard"}
	_, fault := manager.spoken_target(api.Recommendation{Node_id: "de-01", Protocol: "amneziawg",
		Alternatives: []api.Alternative{{Node: "de-01", Protocol: "wireguard", State: state_frozen}}})
	if fault == nil || !strings.Contains(fault.Error(), "picked by hand, wireguard, freezes") {
		t.Fatalf("fault = %v; want the picked protocol named as freezing", fault)
	}
}
