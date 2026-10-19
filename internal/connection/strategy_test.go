package connection

import (
	"context"
	"reflect"
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

func Test_a_first_connect_with_no_place_asked_for_lets_the_server_pick(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Strategy = state.Strategy{Bypass: true, Route: true}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.control_plane.recommend_calls != 0 || env.control_plane.create_profile_nodes[0] != "" {
		t.Fatalf("recommend calls %d, provisioned on %v; want the server's own pick", env.control_plane.recommend_calls, env.control_plane.create_profile_nodes)
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
