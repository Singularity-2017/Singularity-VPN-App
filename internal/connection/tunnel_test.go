package connection

import (
	"context"
	"testing"

	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/state"
)

func Test_what_a_connect_asks_the_control_plane_for(t *testing.T) {
	anti := "antizapret"
	cases := []struct {
		name  string
		local state.State
		want  string
	}{
		{"nothing chosen", state.State{}, Tunnel_vpn},
		{"vpn chosen", state.State{Tunnel_mode: "vpn"}, Tunnel_vpn},
		{"antizapret chosen", state.State{Tunnel_mode: anti}, Tunnel_antizapret},
		{"antizapret and anonymity", state.State{Tunnel_mode: anti, Strategy: state.Strategy{Anonymity: "light"}}, Tunnel_vpn},
		{"a game in full vpn", state.State{Games: []string{"pubg"}}, Tunnel_vpn},
		{"a game beats antizapret", state.State{Games: []string{"pubg"}, Tunnel_mode: anti}, Tunnel_games},
		{"a game and anonymity", state.State{Games: []string{"pubg"}, Tunnel_mode: anti, Strategy: state.Strategy{Anonymity: "high"}}, Tunnel_vpn},
		{"a touchy game and antizapret", state.State{Games: []string{"pubg", "cod"}, Tunnel_mode: anti}, Tunnel_antizapret},
		{"a state file from before the field", state.State{Tunnel_mode: ""}, Tunnel_vpn},
	}
	for _, c := range cases {
		if got := effective_mode(c.local); got != c.want {
			t.Errorf("%s: effective_mode = %q, want %q", c.name, got, c.want)
		}
	}
	if profile_for(Tunnel_antizapret) != "antizapret" || profile_for(Tunnel_vpn) != "" || profile_for(Tunnel_games) != "" {
		t.Error("only the antizapret mode asks for the antizapret profile")
	}
}

func Test_a_connect_in_antizapret_mode_fetches_that_profile_and_says_so(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.store.state.Tunnel_mode = "antizapret"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if got := env.control_plane.asked_modes; len(got) != 1 || got[0] != "antizapret" {
		t.Fatalf("modes asked = %v, want one antizapret", got)
	}
	if session, _ := manager.Session(); session.Tunnel != Tunnel_antizapret {
		t.Fatalf("Session().Tunnel = %q, want antizapret", session.Tunnel)
	}
}

func Test_a_first_connect_in_antizapret_mode_replaces_the_full_config_it_was_given(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Tunnel_mode = "antizapret"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.control_plane.create_profile_calls != 1 {
		t.Fatalf("Create_profile calls = %d, want 1", env.control_plane.create_profile_calls)
	}
	if got := env.control_plane.asked_modes; len(got) != 1 || got[0] != "antizapret" {
		t.Fatalf("modes asked = %v, want the antizapret profile fetched after provisioning", got)
	}
}

func Test_a_default_connect_asks_for_no_mode_and_says_vpn(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if got := env.control_plane.asked_modes; len(got) != 1 || got[0] != "" {
		t.Fatalf("modes asked = %q, want one empty", got)
	}
	if session, _ := manager.Session(); session.Tunnel != Tunnel_vpn {
		t.Fatalf("Session().Tunnel = %q, want vpn", session.Tunnel)
	}
}

func Test_the_tunnel_mode_reads_vpn_until_one_is_set(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if got, err := manager.Tunnel_mode(); err != nil || got != Tunnel_vpn {
		t.Fatalf("Tunnel_mode() = %q, %v; want vpn", got, err)
	}
	if err := manager.Set_tunnel_mode("full"); err == nil {
		t.Fatal("a mode that is not vpn or antizapret was recorded")
	}
	if err := manager.Set_tunnel_mode(Tunnel_antizapret); err != nil {
		t.Fatalf("Set_tunnel_mode() error = %v", err)
	}
	if got, _ := manager.Tunnel_mode(); got != Tunnel_antizapret {
		t.Fatalf("Tunnel_mode() = %q, want antizapret", got)
	}
}

func Test_a_tunnel_mode_set_while_disconnected_is_only_stored(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Set_tunnel_mode(Tunnel_antizapret); err != nil {
		t.Fatalf("Set_tunnel_mode() error = %v", err)
	}
	if err := manager.Apply_tunnel(context.Background()); err != nil {
		t.Fatalf("Apply_tunnel() error = %v", err)
	}
	if env.control_plane.fetch_config_calls != 0 || env.protocol.connect_calls != 0 {
		t.Fatalf("a disconnected manager fetched %d configs and connected %d times, want neither", env.control_plane.fetch_config_calls, env.protocol.connect_calls)
	}
	if env.store.state.Tunnel_mode != "antizapret" {
		t.Fatalf("stored mode = %q, want antizapret", env.store.state.Tunnel_mode)
	}
}

func Test_a_tunnel_mode_set_while_connected_rebuilds_the_tunnel_on_the_same_node(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	before, _ := manager.Session()

	if err := manager.Set_tunnel_mode(Tunnel_antizapret); err != nil {
		t.Fatalf("Set_tunnel_mode() error = %v", err)
	}
	if err := manager.Apply_tunnel(context.Background()); err != nil {
		t.Fatalf("Apply_tunnel() error = %v", err)
	}

	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected", manager.State())
	}
	if env.protocol.connect_calls != 2 || env.protocol.disconnect_calls != 1 {
		t.Fatalf("connects = %d, disconnects = %d; want the tunnel rebuilt once", env.protocol.connect_calls, env.protocol.disconnect_calls)
	}
	if got := env.control_plane.asked_modes; len(got) != 2 || got[1] != "antizapret" {
		t.Fatalf("modes asked = %q, want the antizapret profile fetched for the rebuild", got)
	}
	after, _ := manager.Session()
	if after.Node_id != before.Node_id || after.Protocol != before.Protocol || after.Id != before.Id {
		t.Fatalf("session moved: %+v -> %+v", before, after)
	}
	if after.Tunnel != Tunnel_antizapret {
		t.Fatalf("Session().Tunnel = %q, want antizapret", after.Tunnel)
	}

	// Asking again, with nothing changed, builds nothing.
	if err := manager.Apply_tunnel(context.Background()); err != nil {
		t.Fatalf("Apply_tunnel() error = %v", err)
	}
	if env.protocol.connect_calls != 2 {
		t.Fatalf("connects = %d, want the same tunnel left alone", env.protocol.connect_calls)
	}
}

// A first verification right after the rebuild fails, the retry handle_lost
// makes on the same node lands: the tunnel is back, so the change is not
// reported as a failure.
func Test_a_rebuild_that_comes_back_on_the_second_try_is_not_an_error(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := manager.Set_tunnel_mode(Tunnel_antizapret); err != nil {
		t.Fatalf("Set_tunnel_mode() error = %v", err)
	}
	lost_sample := diagnostics.Sample{Tunnel_up: false}
	env.prober.set([]prober_response{{sample: lost_sample}, {sample: healthy_sample}})

	if err := manager.Apply_tunnel(context.Background()); err != nil {
		t.Fatalf("Apply_tunnel() error = %v, want nil: the tunnel came back", err)
	}
	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected", manager.State())
	}
	if env.protocol.connect_calls != 3 {
		t.Fatalf("connects = %d, want the rebuild and one retry after it", env.protocol.connect_calls)
	}
}
