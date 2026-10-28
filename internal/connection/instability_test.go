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

// drop is one short drop on a connected tunnel: a bad sample the next good
// one does not confirm, so the connection never stays degraded long enough
// for the degradation window.
func drop(env *test_env, manager *Manager, gap time.Duration) {
	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}}})
	manager.Tick(context.Background(), env.clock.advance(gap))
	env.prober.set([]prober_response{{sample: healthy_sample}})
	manager.Tick(context.Background(), env.clock.advance(30*time.Second))
}

func dropping_env(t *testing.T) (*test_env, *Manager) {
	t.Helper()
	env := new_test_env(t)
	env.protocols.by_name["openvpn"] = &fake_protocol{name: "openvpn"}
	env.control_plane.recommend_result.Alternatives = []api.Alternative{{Node: "de-01", Protocol: "openvpn"}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	return env, manager
}

func protocol_now(t *testing.T, manager *Manager) string {
	t.Helper()
	session, ok := manager.Session()
	if !ok {
		t.Fatalf("no session; state %v", manager.State())
	}
	return session.Protocol
}

// A connection that keeps dropping for a moment and coming back was kept
// for hours: no single drop lasts the degradation window. Five drops in
// fifteen minutes move it to another protocol, whatever the person is doing
// on the machine.
func Test_a_connection_that_keeps_dropping_moves_to_another_protocol(t *testing.T) {
	env, manager := dropping_env(t)
	for range 4 {
		drop(env, manager, 2*time.Minute)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("after four drops on %s; want it kept on wireguard", got)
	}
	drop(env, manager, 2*time.Minute)
	if got := protocol_now(t, manager); got != "openvpn" {
		t.Fatalf("after five drops in fifteen minutes on %s; want openvpn", got)
	}
}

// Drops spread over hours are not a connection that keeps dropping.
func Test_drops_hours_apart_move_nothing(t *testing.T) {
	env, manager := dropping_env(t)
	for range 8 {
		drop(env, manager, time.Hour)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("after drops an hour apart on %s; want wireguard kept", got)
	}
}

// The protocol switched to dropping as often says the protocol was not the
// cause: the next move waits an hour instead of trading one for another
// every quarter of an hour, each time breaking what the person had open.
func Test_after_a_move_for_drops_the_next_waits_an_hour(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["openvpn"] = &fake_protocol{name: "openvpn"}
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	env.control_plane.recommend_result.Alternatives = []api.Alternative{{Node: "de-01", Protocol: "openvpn"}, {Node: "de-01", Protocol: "amneziawg"}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	moved := protocol_now(t, manager)
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	first := protocol_now(t, manager)
	if first == moved {
		t.Fatalf("five drops in fifteen minutes left the connection on %s", first)
	}
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	if got := protocol_now(t, manager); got != first {
		t.Fatalf("five drops right after a move landed on %s; want %s kept", got, first)
	}
	manager.Tick(context.Background(), env.clock.advance(45*time.Minute))
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	if got := protocol_now(t, manager); got == first {
		t.Fatalf("an hour on, still dropping, kept on %s; want another protocol", got)
	}
}

// During a match nothing moves a working connection on its own: leaving the
// node would end the match.
func Test_drops_during_a_match_move_nothing(t *testing.T) {
	env, manager := dropping_env(t)
	manager.Set_in_match(true)
	for range 6 {
		drop(env, manager, 2*time.Minute)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("drops during a match moved the connection to %s", got)
	}
}

// What the person picked by hand is not moved.
func Test_drops_on_a_connection_picked_by_hand_move_nothing(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["openvpn"] = &fake_protocol{name: "openvpn"}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_manual, Node_id: "de-01", Protocol: "wireguard"}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	for range 6 {
		drop(env, manager, 2*time.Minute)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("drops moved a connection picked by hand to %s", got)
	}
}

// Drops on the network the machine has left say nothing about the one it is
// on now, and are not added to its drops.
func Test_drops_before_a_network_change_do_not_count_after_it(t *testing.T) {
	env, manager := dropping_env(t)
	for range 3 {
		drop(env, manager, 2*time.Minute)
	}
	manager.Network_changed(context.Background(), env.clock.advance(time.Second))
	for range 3 {
		drop(env, manager, time.Minute)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("drops on two networks added up and moved the connection to %s", got)
	}
}

// A connection that drops now and then still carries traffic. When nothing
// else comes up, the move ends back on it rather than with no connection.
func Test_a_move_for_drops_that_finds_nothing_working_comes_back(t *testing.T) {
	env, manager := dropping_env(t)
	env.protocols.by_name["openvpn"].connect_err = errors.New("no handshake")
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	if state := manager.State(); state != State_connected {
		t.Fatalf("state %v after a move that found nothing; want connected", state)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("ended on %s; want back on wireguard", got)
	}
}

// The way back is not left to the switch budget: with more alternatives
// failing than the budget allows, the move still ends on the connection it
// left rather than with none.
func Test_a_move_for_drops_comes_back_past_many_failing_alternatives(t *testing.T) {
	env := new_test_env(t)
	alternatives := []api.Alternative{}
	for _, node := range []string{"nl-02", "fi-03", "se-04"} {
		alternatives = append(alternatives, api.Alternative{Node: node, Protocol: "openvpn"})
	}
	env.protocols.by_name["openvpn"] = &fake_protocol{name: "openvpn", connect_err: errors.New("no handshake")}
	env.control_plane.recommend_result.Alternatives = alternatives
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	if state := manager.State(); state != State_connected {
		t.Fatalf("state %v after three alternatives failed; want connected", state)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("ended on %s; want back on wireguard", got)
	}
}

// A move that found nowhere to go did not happen, and does not hold back the
// next one for an hour.
func Test_a_move_for_drops_with_nowhere_to_go_does_not_spend_the_hour(t *testing.T) {
	env, manager := dropping_env(t)
	manager.policy.Record_failure(env.clock.now, recovery.Target{Node_id: "de-01", Protocol: "openvpn"})
	for range 5 { // all inside the five-minute cooldown
		drop(env, manager, 20*time.Second)
	}
	if got := protocol_now(t, manager); got != "wireguard" {
		t.Fatalf("moved to %s while it was cooling down", got)
	}
	for range 5 {
		drop(env, manager, time.Minute)
	}
	if got := protocol_now(t, manager); got != "openvpn" {
		t.Fatalf("on %s once the cooldown was over; want openvpn, the refused move spent nothing", got)
	}
}

// The protocol moved to starts with a clean slate: its first bad sample is
// a blip, not a confirmation of the last one on the protocol it left.
func Test_the_first_bad_sample_after_a_move_is_a_blip(t *testing.T) {
	env, manager := dropping_env(t)
	for range 4 {
		drop(env, manager, 2*time.Minute)
	}
	// The fifth drop moves the connection, and the very next sample, on the
	// new protocol, is bad too.
	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}}})
	manager.Tick(context.Background(), env.clock.advance(2*time.Minute))
	if got := protocol_now(t, manager); got != "openvpn" {
		t.Fatalf("on %s after the fifth drop; want openvpn", got)
	}
	manager.Tick(context.Background(), env.clock.advance(time.Minute))
	if state := manager.State(); state != State_connected {
		t.Fatalf("state %v after one bad sample on the new protocol; want connected, a blip", state)
	}
}

// The hour between moves is about the network the move was made on; on
// another network the drops are judged afresh.
func Test_a_network_change_lifts_the_hour_between_moves(t *testing.T) {
	env := new_test_env(t)
	env.protocols.by_name["openvpn"] = &fake_protocol{name: "openvpn"}
	env.protocols.by_name["amneziawg"] = &fake_protocol{name: "amneziawg"}
	env.control_plane.recommend_result.Alternatives = []api.Alternative{{Node: "de-01", Protocol: "openvpn"}, {Node: "de-01", Protocol: "amneziawg"}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	first := protocol_now(t, manager)
	manager.Network_changed(context.Background(), env.clock.advance(time.Minute))
	for range 5 {
		drop(env, manager, 2*time.Minute)
	}
	if got := protocol_now(t, manager); got == first {
		t.Fatalf("five drops on a new network kept %s; want another protocol", got)
	}
}
