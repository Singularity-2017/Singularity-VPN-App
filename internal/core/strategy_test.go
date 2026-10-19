package core

import (
	"context"
	"errors"
	"testing"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/ipc"
)

func Test_a_strategy_is_kept_and_reported_back(t *testing.T) {
	f := new_fixture(t)
	want := ipc.Strategy{Anonymity: "standard", Bypass: true, Country: "DE"}
	if err := f.app.Set_strategy(context.Background(), want); err != nil {
		t.Fatalf("Set_strategy() error = %v", err)
	}
	if got, err := f.app.Strategy(context.Background()); err != nil || got != want {
		t.Fatalf("Strategy() = %+v, %v; want %+v", got, err, want)
	}
	if err := f.app.Set_strategy(context.Background(), ipc.Strategy{Anonymity: "total"}); err == nil {
		t.Fatal("an unknown anonymity level was accepted")
	}
	if got, _ := f.app.Strategy(context.Background()); got != want {
		t.Fatalf("a refused strategy changed the stored one to %+v", got)
	}
}

func Test_a_status_says_how_the_control_plane_chose_the_node(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)
	reconnect_through_recommend(t, f, api.Recommendation{
		Node_id: "node-1", Protocol: "wireguard", Reason: "lowest_rtt",
		Goals: []api.Goal_outcome{{Goal: "anonymity", Why: "not_supported"}, {Goal: "route", Applied: true, Compared: 3}},
	})
	got, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "lowest_rtt" || len(got.Goals) != 2 || got.Goals[0].Why != "not_supported" || got.Goals[1].Compared != 3 {
		t.Fatalf("Status() reason %q goals %+v; want them as the control plane gave them", got.Reason, got.Goals)
	}
}

func Test_a_country_with_no_server_reaches_the_window_by_name(t *testing.T) {
	err := map_error(&api.Error{Code: api.Code_no_node_in_country})
	if !errors.Is(err, ipc.Err_no_server_in_country) {
		t.Fatalf("map_error(NO_NODE_IN_COUNTRY) = %v", err)
	}
	if err := map_error(&api.Error{Code: api.Code_node_unavailable}); !errors.Is(err, ipc.Err_server_unavailable) {
		t.Fatalf("map_error(NODE_UNAVAILABLE) = %v", err)
	}
}

func Test_the_server_list_needs_a_signed_in_device_and_passes_through(t *testing.T) {
	f := new_fixture(t)
	if _, err := f.app.Servers(context.Background()); !errors.Is(err, ipc.Err_not_signed_in) {
		t.Fatalf("Servers() before sign-in = %v, want not signed in", err)
	}
	sign_in(t, f)
	f.control_plane.servers = []api.Server{{Node_id: "node-2", Name: "Germany 1", Country: "DE", Available: true}}
	got, err := f.app.Servers(context.Background())
	if err != nil || len(got.Servers) != 1 || got.Servers[0].Country != "DE" || !got.Servers[0].Available {
		t.Fatalf("Servers() = %+v, %v", got, err)
	}
}

// A server picked by hand is the signed-out account's; the next account
// may not be allowed on it. The goals belong to the machine and stay.
func Test_sign_out_forgets_the_server_picked_by_hand_and_keeps_the_goals(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	if err := f.app.Set_strategy(context.Background(), ipc.Strategy{Bypass: true, Node_id: "node-2"}); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Sign_out(context.Background()); err != nil {
		t.Fatalf("Sign_out() error = %v", err)
	}
	if got, _ := f.app.Strategy(context.Background()); got.Node_id != "" || !got.Bypass {
		t.Fatalf("strategy after sign-out = %+v; want the server forgotten and bypass kept", got)
	}
}
