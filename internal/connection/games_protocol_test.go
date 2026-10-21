package connection

import (
	"context"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/protocols"
)

// whole_machine_protocol is a fake_protocol that carries the whole machine
// whatever the split asks, as OpenVPN does.
type whole_machine_protocol struct{ *fake_protocol }

func (whole_machine_protocol) Whole_machine_only() {}

// games_env is a test_env whose node offers OpenVPN, which carries the whole
// machine, beside WireGuard and AmneziaWG, which can carry the games alone.
func games_env(t *testing.T, alternatives []api.Alternative) *test_env {
	env := new_test_env(t)
	env.protocols.by_name["openvpn"] = &fake_protocol{name: "openvpn"}
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	env.protocols.wrap = func(p *fake_protocol) protocols.Protocol {
		if p.name == "openvpn" {
			return whole_machine_protocol{p}
		}
		return p
	}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "de-01", Protocol: "openvpn", Alternatives: alternatives}
	return env
}

func Test_boosting_a_game_on_openvpn_moves_to_a_protocol_of_the_same_node_that_carries_it_alone(t *testing.T) {
	env := games_env(t, []api.Alternative{
		{Node: "nl-02", Protocol: "wireguard"},
		{Node: "de-01", Protocol: "amneziawg", State: state_frozen},
		{Node: "de-01", Protocol: "wireguard"},
	})
	env.control_plane.create_profile_result.Protocol = "openvpn"
	env.store.state.Tunnel_mode = Tunnel_antizapret
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if session, _ := manager.Session(); session.Protocol != "openvpn" {
		t.Fatalf("without games the session is on %q, want openvpn", session.Protocol)
	}
	if err := manager.Set_games([]string{"pubg"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Apply_games(context.Background()); err != nil {
		t.Fatalf("Apply_games() error = %v", err)
	}
	session, _ := manager.Session()
	if session.Node_id != "de-01" || session.Protocol != "wireguard" || session.Tunnel != Tunnel_games {
		t.Fatalf("after the boost the session is %s over %s carrying %q, want de-01 over wireguard carrying games", session.Node_id, session.Protocol, session.Tunnel)
	}
}

func Test_connecting_with_a_game_chosen_skips_a_recommended_protocol_that_carries_the_whole_machine(t *testing.T) {
	env := games_env(t, []api.Alternative{{Node: "de-01", Protocol: "amneziawg"}, {Node: "de-01", Protocol: "wireguard"}})
	env.store.state.Tunnel_mode = Tunnel_antizapret
	manager := env.build()
	if err := manager.Set_games([]string{"pubg"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if got := env.control_plane.asked_protocols[0]; got != "amneziawg" {
		t.Fatalf("asked the control plane for %q, want amneziawg, the first offer on the node that carries the game alone", got)
	}
}

func Test_a_node_with_nothing_but_openvpn_keeps_the_whole_machine_on_it(t *testing.T) {
	env := games_env(t, []api.Alternative{{Node: "nl-02", Protocol: "wireguard"}})
	env.control_plane.create_profile_result.Protocol = "openvpn"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := manager.Set_games([]string{"pubg"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Apply_games(context.Background()); err != nil {
		t.Fatalf("Apply_games() error = %v", err)
	}
	if session, _ := manager.Session(); session.Node_id != "de-01" || session.Protocol != "openvpn" {
		t.Fatalf("the session moved to %s over %s, want to stay on de-01 over openvpn", session.Node_id, session.Protocol)
	}
}

func Test_antizapret_runs_beside_a_boosted_game_and_goes_with_it(t *testing.T) {
	env := games_env(t, []api.Alternative{{Node: "de-01", Protocol: "amneziawg"}, {Node: "de-01", Protocol: "openvpn"}})
	env.store.state.Tunnel_mode = Tunnel_antizapret
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Set_games([]string{"pubg"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	manager.Tick(context.Background(), env.clock.advance(5*time.Second)) // the companion comes up on a tick
	session, _ := manager.Session()
	openvpn := env.protocols.by_name["openvpn"]
	if session.Tunnel != Tunnel_games || session.Protocol != "amneziawg" {
		t.Fatalf("session = %s carrying %q, want amneziawg carrying games", session.Protocol, session.Tunnel)
	}
	if openvpn.connect_calls != 1 || env.control_plane.asked_modes[len(env.control_plane.asked_modes)-1] != "antizapret" {
		t.Fatalf("openvpn connects = %d, last mode asked %v; want AntiZapret brought up beside the games", openvpn.connect_calls, env.control_plane.asked_modes)
	}
	if err := manager.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if openvpn.disconnect_calls == 0 {
		t.Fatal("the AntiZapret tunnel was left up after disconnecting")
	}
}

func Test_without_antizapret_chosen_a_boost_runs_alone(t *testing.T) {
	env := games_env(t, []api.Alternative{{Node: "de-01", Protocol: "amneziawg"}, {Node: "de-01", Protocol: "openvpn"}})
	manager := env.build()
	if err := manager.Set_games([]string{"pubg"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if n := env.protocols.by_name["openvpn"].connect_calls; n != 0 {
		t.Fatalf("openvpn connects = %d, want none: AntiZapret was not chosen", n)
	}
}

func Test_the_antizapret_companion_gives_way_and_goes_when_the_games_do(t *testing.T) {
	env := games_env(t, []api.Alternative{{Node: "de-01", Protocol: "amneziawg"}, {Node: "de-01", Protocol: "openvpn"}})
	env.store.state.Tunnel_mode = Tunnel_antizapret
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Set_games([]string{"pubg"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	openvpn := env.protocols.by_name["openvpn"]
	if manager.companion.handle == nil {
		t.Fatal("no companion after a tick")
	}
	// The main tunnel about to connect over OpenVPN takes its adapter.
	manager.yield_adapter(context.Background(), manager.companion.handle)
	if manager.companion.handle != nil || openvpn.disconnect_calls == 0 {
		t.Fatal("the companion kept the OpenVPN adapter from the main tunnel")
	}
	// Up again, then the games are cleared: it goes.
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	if err := manager.Set_games(nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Apply_games(context.Background()); err != nil {
		t.Fatalf("Apply_games() error = %v", err)
	}
	if manager.companion.handle != nil {
		t.Fatal("the companion stayed after the games were cleared")
	}
}
