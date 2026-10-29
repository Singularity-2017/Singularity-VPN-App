package connection

import (
	"context"
	"errors"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/recovery"
)

// shared_tunnel is one adapter that several fake protocols run on, as
// WireGuard and AmneziaWG do.
type shared_tunnel struct {
	handed, released int
	// fetches_when_handed is how many configs had been fetched when the
	// tunnel was handed over.
	fetches_when_handed int
	fetches             func() int
}

// handing_protocol is a fake_protocol on a shared_tunnel: a
// protocols.Hand_over.
type handing_protocol struct {
	*fake_protocol
	tunnel *shared_tunnel
}

func (h handing_protocol) Carries(next protocols.Protocol) bool {
	other, ok := next.(handing_protocol)
	return ok && other.tunnel == h.tunnel
}

func (h handing_protocol) Hand_over(ctx context.Context) func(context.Context) error {
	if !h.running {
		return func(context.Context) error { return nil }
	}
	h.running = false
	h.tunnel.handed++
	h.tunnel.fetches_when_handed = h.tunnel.fetches()
	return func(context.Context) error {
		h.tunnel.released++
		return nil
	}
}

// hand_over_env is a test_env connected over WireGuard in tunnel_mode,
// with AmneziaWG on the same adapter.
func hand_over_env(t *testing.T, tunnel_mode string) (*test_env, *Manager, *shared_tunnel, *fake_protocol) {
	t.Helper()
	env := new_test_env(t)
	env.store.state.Tunnel_mode = tunnel_mode
	amnezia := &fake_protocol{name: "amneziawg"}
	env.protocols.by_name["amneziawg"] = amnezia
	tunnel := &shared_tunnel{fetches: func() int { return env.control_plane.fetch_config_calls }}
	env.protocols.wrap = func(p *fake_protocol) protocols.Protocol { return handing_protocol{p, tunnel} }
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	return env, manager, tunnel, amnezia
}

// A move a person asks for, from a tunnel that works, keeps the adapter up:
// the config comes through the tunnel still carrying everything, then the
// tunnel goes to AmneziaWG on the same adapter instead of coming down.
func Test_a_switch_to_a_protocol_on_the_same_tunnel_hands_it_over_once_the_config_is_in(t *testing.T) {
	env, manager, tunnel, amnezia := hand_over_env(t, Tunnel_vpn)
	session, _ := manager.Session()
	fetched := env.control_plane.fetch_config_calls

	if err := manager.Switch(context.Background(), recovery.Target{Node_id: session.Node_id, Protocol: "amneziawg"}); err != nil {
		t.Fatalf("Switch() error = %v", err)
	}
	if tunnel.handed != 1 || env.protocol.disconnect_calls != 0 || amnezia.connect_calls != 1 || tunnel.released != 1 {
		t.Fatalf("handed over %d times, disconnected %d, AmneziaWG connected %d, released %d; want one hand-over taken up and released",
			tunnel.handed, env.protocol.disconnect_calls, amnezia.connect_calls, tunnel.released)
	}
	if tunnel.fetches_when_handed != fetched+1 {
		t.Fatalf("%d configs fetched when the tunnel was handed over, want %d: the new config first", tunnel.fetches_when_handed, fetched+1)
	}
}

func Test_a_hand_over_whose_new_connection_fails_is_released(t *testing.T) {
	env, manager, tunnel, amnezia := hand_over_env(t, Tunnel_vpn)
	session, _ := manager.Session()
	amnezia.connect_err = errors.New("no handshake")

	_ = manager.Switch(context.Background(), recovery.Target{Node_id: session.Node_id, Protocol: "amneziawg"})
	if tunnel.handed != 1 || tunnel.released != 1 || env.protocol.disconnect_calls != 0 {
		t.Fatalf("handed over %d times, released %d, disconnected %d; want the hand-over released", tunnel.handed, tunnel.released, env.protocol.disconnect_calls)
	}
}

// A config that does not come through the tunnel may mean the tunnel is
// what failed: it comes down, and the switch goes on as it always has.
func Test_a_switch_whose_config_does_not_come_takes_the_tunnel_down(t *testing.T) {
	env, manager, tunnel, _ := hand_over_env(t, Tunnel_vpn)
	session, _ := manager.Session()
	env.control_plane.fetch_config_err = errors.New("timeout")

	_ = manager.Switch(context.Background(), recovery.Target{Node_id: session.Node_id, Protocol: "amneziawg"})
	if tunnel.handed != 0 || env.protocol.disconnect_calls != 1 {
		t.Fatalf("handed over %d times and disconnected %d, want the tunnel taken down once", tunnel.handed, env.protocol.disconnect_calls)
	}
}

// A tunnel that lost the connection is what failed: it is rebuilt from
// nothing, not handed over.
func Test_a_lost_tunnel_is_taken_down_not_handed_over(t *testing.T) {
	env, manager, tunnel, _ := hand_over_env(t, Tunnel_vpn)
	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: false}}, {sample: healthy_sample}})

	manager.Tick(context.Background(), env.clock.advance(5*time.Second))

	if tunnel.handed != 0 || env.protocol.disconnect_calls != 1 || env.protocol.connect_calls != 2 {
		t.Fatalf("handed over %d times, disconnected %d, connected %d; want the lost tunnel taken down and retried", tunnel.handed, env.protocol.disconnect_calls, env.protocol.connect_calls)
	}
}

// Turning a boost on rebuilds the tunnel on the same protocol with what it
// carries changed: handed over, it changes only the routes.
func Test_a_boost_rebuilds_the_tunnel_by_handing_it_over(t *testing.T) {
	env, manager, tunnel, _ := hand_over_env(t, Tunnel_antizapret)
	if err := manager.Set_games([]string{"pubg"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Apply_games(context.Background()); err != nil {
		t.Fatalf("Apply_games() error = %v", err)
	}
	if tunnel.handed != 1 || env.protocol.disconnect_calls != 0 || env.protocol.connect_calls != 2 {
		t.Fatalf("handed over %d times, disconnected %d, connected %d; want the rebuild to hand the tunnel over", tunnel.handed, env.protocol.disconnect_calls, env.protocol.connect_calls)
	}
}

// then_fetches makes the fetches after those already made answer responses
// in turn, and every later one as before.
func then_fetches(env *test_env, responses ...fetch_config_response) {
	ok := fetch_config_response{config: env.control_plane.fetch_config_result}
	sequence := make([]fetch_config_response, env.control_plane.fetch_config_calls)
	for i := range sequence {
		sequence[i] = ok
	}
	env.control_plane.fetch_config_sequence = append(sequence, append(responses, ok)...)
}

// A config that does not come through the tunnel may mean the tunnel is what
// failed, not the server the person picked: the tunnel comes down, and the
// config is asked for again over the network left.
func Test_a_config_that_does_not_come_through_the_tunnel_is_asked_for_again_without_it(t *testing.T) {
	env, manager, tunnel, amnezia := hand_over_env(t, Tunnel_vpn)
	session, _ := manager.Session()
	then_fetches(env, fetch_config_response{err: errors.New("context deadline exceeded")})

	if err := manager.Switch(context.Background(), recovery.Target{Node_id: session.Node_id, Protocol: "amneziawg"}); err != nil {
		t.Fatalf("Switch() error = %v", err)
	}
	if after, _ := manager.Session(); after.Protocol != "amneziawg" || amnezia.connect_calls != 1 {
		t.Fatalf("session on %q after %d AmneziaWG connects, want the server picked, connected once", after.Protocol, amnezia.connect_calls)
	}
	if tunnel.handed != 0 || env.protocol.disconnect_calls != 1 {
		t.Fatalf("handed over %d times and disconnected %d, want the tunnel taken down once", tunnel.handed, env.protocol.disconnect_calls)
	}
}

// An answer is an answer: the control plane refusing through the tunnel
// shows the tunnel works, and asking again without it changes nothing.
func Test_a_control_plane_that_answers_through_the_tunnel_is_not_asked_again(t *testing.T) {
	env, manager, _, _ := hand_over_env(t, Tunnel_vpn)
	session, _ := manager.Session()
	refusal := &api.Error{Status: 409, Code: "node_unavailable", Message: "the node is full"}
	then_fetches(env, fetch_config_response{err: refusal})
	fetched := env.control_plane.fetch_config_calls

	_ = manager.Switch(context.Background(), recovery.Target{Node_id: session.Node_id, Protocol: "amneziawg"})
	if asked := env.control_plane.fetch_config_calls - fetched; asked != 1 {
		t.Fatalf("the config was asked for %d times, want once", asked)
	}
}

// Recovery moves on from what failed: a tunnel that degraded comes down,
// even when the next protocol runs on its adapter.
func Test_a_switch_away_from_a_degraded_tunnel_takes_it_down(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	tunnel := &shared_tunnel{fetches: func() int { return env.control_plane.fetch_config_calls }}
	env.protocols.wrap = func(p *fake_protocol) protocols.Protocol { return handing_protocol{p, tunnel} }
	env.control_plane.recommend_result.Alternatives = []api.Alternative{{Node: "nl-02", Protocol: "amneziawg"}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	manager.Tick(context.Background(), env.clock.advance(40*time.Second))
	manager.Tick(context.Background(), env.clock.advance(16*time.Second))

	if session, _ := manager.Session(); session.Node_id != "nl-02" || session.Protocol != "amneziawg" {
		t.Fatalf("session on %s over %s, want the switch to have landed on nl-02 over amneziawg", session.Node_id, session.Protocol)
	}
	if tunnel.handed != 0 || env.protocol.disconnect_calls != 1 {
		t.Fatalf("handed over %d times and disconnected %d, want the degraded tunnel taken down", tunnel.handed, env.protocol.disconnect_calls)
	}
}
