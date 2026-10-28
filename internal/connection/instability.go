package connection

import (
	"context"
	"slices"
	"time"

	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/recovery"
)

// A connection can keep dropping for a moment and coming back without ever
// staying degraded for the degradation window: a burst of lost packets, a
// good sample, another burst. Each drop is a blip, and a connection made of
// blips was kept for hours -- OpenVPN, 22 blips in 13 hours, eight of them
// in eleven minutes (2026-10-28). Drops that come too often are a reason to
// move to another protocol on their own, whatever the person is doing on the
// machine: what they have open may break once, which is better than breaking
// every few minutes (owner, 2026-10-28).
const (
	unstable_drops  = 5
	unstable_window = 15 * time.Minute
	// unstable_gap is how long after one move for drops the next waits. The
	// protocol moved to dropping as often says the protocol was not the
	// cause, and trading one for another every quarter of an hour would
	// break what the person has open each time for nothing.
	unstable_gap = time.Hour
)

// note_drop records one short drop of the current connection and moves it
// when they come too often. The caller holds op_mu.
func (m *Manager) note_drop(ctx context.Context, now time.Time) {
	cutoff := now.Add(-unstable_window)
	m.drops = append(slices.DeleteFunc(m.drops, func(at time.Time) bool { return at.Before(cutoff) }), now)
	if len(m.drops) < unstable_drops || m.mode != Mode_auto || m.in_match.Load() {
		return
	}
	m.drops = nil
	if !m.unstable_moved_at.IsZero() && now.Sub(m.unstable_moved_at) < unstable_gap {
		m.logger.Info("the connection keeps dropping after a move for drops; staying", "node_id", m.current.Node_id, "protocol", m.current.Protocol)
		return
	}
	next, err := m.policy.Next_target(now, recovery.Reason_degraded, m.current, m.holding(m.alternatives))
	if err != nil {
		m.logger.Info("the connection keeps dropping; there is nothing else to move to", "node_id", m.current.Node_id, "protocol", m.current.Protocol, "reason", err.Error())
		return
	}
	m.unstable_moved_at = now
	m.logger.Info("the connection keeps dropping; moving to another protocol", "node_id", m.current.Node_id, "protocol", m.current.Protocol,
		"next_node_id", next.Node_id, "next_protocol", next.Protocol)
	// Dropping, it still carried traffic, however soon after connecting: it
	// is the way back if the move does not hold.
	m.note_held(m.current)
	m.set_state(State_switching)
	m.publish(events.Kind_switch_started, next)
	_ = m.switch_to(ctx, now, next, true)
}
