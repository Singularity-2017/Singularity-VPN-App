package connection

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"singularity-vpn-app/internal/api"
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
