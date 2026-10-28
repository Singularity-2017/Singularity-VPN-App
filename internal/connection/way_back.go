package connection

import (
	"context"
	"slices"
	"time"

	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/recovery"
)

// hold_time is how long a tunnel carries traffic before it counts as one
// that held. A path that lets the handshake through and cuts the tunnel --
// plain WireGuard under DPI -- does it within seconds to a minute; on
// 2026-10-28 it took eighteen.
const hold_time = 5 * time.Minute

// note_carrying records that the current tunnel answered a probe: one that
// has done so for hold_time held. The caller holds op_mu.
func (m *Manager) note_carrying(now time.Time, verdict diagnostics.Verdict) {
	if verdict != diagnostics.Verdict_lost && now.Sub(m.connected_at) >= hold_time {
		m.note_held(m.current)
	}
}

// note_held puts target last, as the newest, in the targets that held.
func (m *Manager) note_held(target recovery.Target) {
	m.held = append(slices.DeleteFunc(m.held, func(t recovery.Target) bool { return t == target }), target)
}

// forget_way_back drops what this connection learned about which targets
// hold: a new connect, a disconnect and a person's new goals or split start
// over, since a target that held under the old ones may not be allowed under
// the new.
func (m *Manager) forget_way_back() {
	m.held, m.did_not_hold = nil, nil
}

// way_back is the newest target that held and may be tried now, other than
// failing. The caller holds op_mu.
func (m *Manager) way_back(now time.Time, failing recovery.Target) (recovery.Target, bool) {
	for i := len(m.held) - 1; i >= 0; i-- {
		if t := m.held[i]; t != failing && m.policy.Eligible(now, t) {
			return t, true
		}
	}
	return recovery.Target{}, false
}

// go_back returns the connection to the newest target that held when the one
// it is on was lost before it held itself: what carried traffic is a better
// bet than anything new (R-ZWP9, 2026-10-28: AmneziaWG, dropping but working,
// was left for WireGuard, cut eighteen seconds later; OpenVPN did not come up
// after it, and the machine stayed without a connection for 55 minutes). It
// reports whether it went back. The caller holds op_mu.
func (m *Manager) go_back(ctx context.Context, now time.Time, reason recovery.Reason) bool {
	if reason != recovery.Reason_hard_fail || slices.Contains(m.held, m.current) {
		return false
	}
	back, ok := m.way_back(now, m.current)
	if !ok {
		return false
	}
	left := m.current
	if !slices.Contains(m.did_not_hold, left) {
		m.did_not_hold = append(m.did_not_hold, left)
	}
	m.logger.Info("the connection moved to did not hold; going back", "node_id", left.Node_id, "protocol", left.Protocol,
		"back_node_id", back.Node_id, "back_protocol", back.Protocol)
	m.set_state(State_switching)
	m.publish(events.Kind_switch_started, back)
	_ = m.switch_to(ctx, now, back, true)
	return true
}

// holding is the offers less what did not hold on this network: a working
// connection is not left for one of them.
func (m *Manager) holding(offers []recovery.Target) []recovery.Target {
	return slices.DeleteFunc(slices.Clone(offers), func(t recovery.Target) bool { return slices.Contains(m.did_not_hold, t) })
}

// holding_first is the offers with what did not hold on this network moved
// last: a connection already lost tries everything else first, and still has
// those rather than nothing.
func (m *Manager) holding_first(offers []recovery.Target) []recovery.Target {
	kept := m.holding(offers)
	for _, t := range offers {
		if !slices.Contains(kept, t) {
			kept = append(kept, t)
		}
	}
	return kept
}

// gave_up journals that automatic recovery found nothing that carries
// traffic: the machine is left without a connection until the person
// connects again.
func (m *Manager) gave_up(fault *Fault) {
	m.logger.Warn("recovery gave up; no connection", "error", fault.Error())
}
