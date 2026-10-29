package connection

import (
	"context"
	"errors"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/recovery"
)

// moved_for_drops connects on wireguard, which then drops five times in
// fifteen minutes and is moved to amneziawg; openvpn is the other offer on
// the same server, and others are offered after both.
func moved_for_drops(t *testing.T, others ...api.Alternative) (*test_env, *Manager) {
	t.Helper()
	env := new_test_env(t)
	env.protocols.by_name["openvpn"] = &fake_protocol{name: "openvpn"}
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	env.control_plane.recommend_result.Alternatives = append([]api.Alternative{{Node: "de-01", Protocol: "openvpn"}, {Node: "de-01", Protocol: "amneziawg"}}, others...)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	if got := protocol_now(t, manager); got != "amneziawg" {
		t.Fatalf("five drops left the connection on %s; want amneziawg", got)
	}
	return env, manager
}

// lose_moved_to makes the connection moved to stop carrying traffic: the next
// probe finds it lost and it cannot be rebuilt.
func lose_moved_to(env *test_env, manager *Manager, after time.Duration) {
	env.protocols.by_name["amneziawg"].connect_err = errors.New("no handshake")
	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: false}}, {sample: healthy_sample}})
	manager.Tick(context.Background(), env.clock.advance(after))
}

// A move for drops left a connection that carried traffic. When the one moved
// to is lost before it held, the connection goes back to the one that held,
// before anything new -- R-ZWP9, 2026-10-28: AmneziaWG, dropping but working,
// was left for WireGuard, cut eighteen seconds later; OpenVPN did not come up
// after it, and the machine stayed without a connection for 55 minutes.
func Test_a_move_that_did_not_hold_goes_back_to_what_held(t *testing.T) {
	env, manager := moved_for_drops(t)
	env.protocols.by_name["openvpn"].connect_err = errors.New("no handshake")
	lose_moved_to(env, manager, 10*time.Second)
	if state := manager.State(); state != State_connected {
		t.Fatalf("state %v after the moved-to connection was lost; want connected, back where it held", state)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("on %s; want back on wireguard, the connection that held", got)
	}
	if calls := env.protocols.by_name["openvpn"].connect_calls; calls != 0 {
		t.Errorf("openvpn was tried %d times before going back; want the way back first", calls)
	}
}

// Two protocols of one server failing in turn say the server is the trouble,
// not the protocol: a move that did not hold goes to another server when one
// is offered, before back to the connection that was dropping. R-7XPN,
// 2026-10-29: AmneziaWG, WireGuard and OpenVPN on one server, round and
// round, and the server never changed.
func Test_a_move_that_did_not_hold_goes_to_another_server_first(t *testing.T) {
	env, manager := moved_for_drops(t, api.Alternative{Node: "nl-02", Protocol: "wireguard"})
	env.protocols.by_name["openvpn"].connect_err = errors.New("no handshake")
	lose_moved_to(env, manager, 10*time.Second)
	session, ok := manager.Session()
	if !ok || session.Node_id != "nl-02" || session.Protocol != "wireguard" {
		t.Fatalf("session %+v (ok %v, state %v); want wireguard on nl-02, another server", session, ok, manager.State())
	}
	if calls := env.protocols.by_name["openvpn"].connect_calls; calls != 0 {
		t.Errorf("openvpn on the same server was tried %d times; want another server first", calls)
	}
}

// Another server that does not come up does not cost the way back: the
// connection still ends on what held, not without one.
func Test_another_server_that_does_not_come_up_still_leaves_the_way_back(t *testing.T) {
	env, manager := moved_for_drops(t, api.Alternative{Node: "nl-02", Protocol: "amneziawg"})
	env.protocols.by_name["openvpn"].connect_err = errors.New("no handshake")
	before := env.protocols.by_name["amneziawg"].connect_calls
	lose_moved_to(env, manager, 10*time.Second)
	if env.protocols.by_name["amneziawg"].connect_calls == before {
		t.Errorf("amneziawg on nl-02 was never tried")
	}
	session, ok := manager.Session()
	if !ok || session.Node_id != "de-01" || session.Protocol != "wireguard" {
		t.Fatalf("session %+v (ok %v, state %v); want back on wireguard on de-01", session, ok, manager.State())
	}
	if calls := env.protocols.by_name["openvpn"].connect_calls; calls != 0 {
		t.Errorf("openvpn on the lost server was tried %d times between the other server and the way back", calls)
	}
}

// What did not hold is not moved to again on the same network: the next move
// for drops, an hour on, takes another protocol.
func Test_a_move_for_drops_does_not_return_to_what_did_not_hold(t *testing.T) {
	env, manager := moved_for_drops(t)
	lose_moved_to(env, manager, 10*time.Second)
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("on %s; want back on wireguard", got)
	}
	env.protocols.by_name["amneziawg"].connect_err = nil
	manager.Tick(context.Background(), env.clock.advance(time.Hour))
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	if got := protocol_now(t, manager); got != "openvpn" {
		t.Fatalf("the next move for drops went to %s; want openvpn, amneziawg did not hold", got)
	}
}

// On another network what did not hold is judged afresh.
func Test_a_network_change_forgets_what_did_not_hold(t *testing.T) {
	env, manager := moved_for_drops(t)
	lose_moved_to(env, manager, 10*time.Second)
	env.protocols.by_name["amneziawg"].connect_err = nil
	manager.Network_changed(context.Background(), env.clock.advance(time.Hour))
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	if got := protocol_now(t, manager); got != "amneziawg" {
		t.Fatalf("on a new network the move for drops went to %s; want amneziawg, the first offer", got)
	}
}

// A way back that is cooling down -- it failed moments ago -- is not taken
// before anything else: it would cost a whole connect timeout for nothing.
func Test_a_way_back_that_is_cooling_down_is_not_taken_first(t *testing.T) {
	env, manager := moved_for_drops(t)
	manager.policy.Record_failure(env.clock.now, recovery.Target{Node_id: "de-01", Protocol: "wireguard"})
	before := env.protocol.connect_calls
	lose_moved_to(env, manager, 10*time.Second)
	if got := protocol_now(t, manager); got != "openvpn" {
		t.Fatalf("on %s; want openvpn, the way back was cooling down", got)
	}
	if env.protocol.connect_calls != before {
		t.Errorf("wireguard was tried while cooling down")
	}
}

// A move that held for hold_time is not reversed when it is lost later: the
// other offers come first. The connection it left still held, though, and is
// the way back when they fail -- not the end of the connection.
func Test_a_move_that_held_is_not_reversed_and_still_has_a_way_back(t *testing.T) {
	env, manager := moved_for_drops(t)
	manager.Tick(context.Background(), env.clock.advance(hold_time+time.Minute))
	env.protocols.by_name["openvpn"].connect_err = errors.New("no handshake")
	lose_moved_to(env, manager, 10*time.Second)
	if calls := env.protocols.by_name["openvpn"].connect_calls; calls == 0 {
		t.Errorf("openvpn was never tried; a move that held is not reversed first")
	}
	if state := manager.State(); state != State_connected {
		t.Fatalf("state %v; want connected, back on the first connection that held", state)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("on %s; want wireguard", got)
	}
}

// A switch a person asked for is not ended somewhere they did not ask for:
// it fails, and says so.
func Test_a_switch_a_person_asked_for_does_not_end_on_the_way_back(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["openvpn"] = &fake_protocol{name: "openvpn", connect_err: errors.New("no handshake")}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	manager.Tick(context.Background(), env.clock.advance(hold_time+time.Minute))
	if err := manager.Switch(context.Background(), recovery.Target{Node_id: "de-01", Protocol: "openvpn"}); err == nil {
		t.Fatal("Switch() to a protocol that did not come up returned no error")
	}
	if state := manager.State(); state != State_failed {
		t.Fatalf("state %v; want failed, not quietly back on wireguard", state)
	}
}
