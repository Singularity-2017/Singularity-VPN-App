package connection

import (
	"context"
	"errors"
	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/evidence"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/recovery"
	"time"
)

// Evidence_mode names a tunnel profile the way the control plane does:
// antizapret is "selective", games is "games", anything else "full". A problem
// report names the connection the same way the observations do.
func Evidence_mode(mode string) string {
	switch mode {
	case Tunnel_games:
		return "games"
	case Tunnel_antizapret:
		return "selective"
	default:
		return "full"
	}
}

// Observation_mode is the mode an observation made now would carry: the
// effective mode of the stored choices, named as the control plane names it.
// A problem report says it the same way, so the control plane can match the
// two. It reads the store, which is safe from any goroutine, not the working
// copy the connection code holds under its own lock.
func (m *Manager) Observation_mode() (string, error) {
	local, err := m.store.Load()
	if err != nil {
		return "", err
	}
	return Evidence_mode(effective_mode(local)), nil
}

func (m *Manager) prepare_evidence(ctx context.Context) {
	if m.evidence == nil {
		return
	}
	current := m.get_state()
	if current != State_disconnected && current != State_failed {
		return
	}
	m.evidence.Prepare(ctx)
}
func (m *Manager) failed_observation(ctx context.Context, target recovery.Target, err error, verification bool) {
	if m.evidence == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	reason := "local_setup"
	var failure *protocols.Path_failure
	if errors.As(err, &failure) {
		reason = failure.Reason
	}
	if verification {
		m.evidence.Network_changed(ctx)
		reason = "unknown"
		if errors.Is(err, diagnostics.Err_no_internet) {
			reason = "reachability_lost"
		}
	}
	m.evidence.Record(ctx, api.Connection_observation{Client_id: m.local_state.Client_id, Node_id: target.Node_id, Protocol: target.Protocol, Mode: Evidence_mode(effective_mode(m.local_state)), Session_id: evidence.New_id(), Kind: "connection_failed", Reason: reason})
}
func (m *Manager) established_observation(ctx context.Context, target recovery.Target) {
	if m.evidence == nil {
		return
	}
	m.evidence_session_id = evidence.New_id()
	m.evidence_started = m.now()
	m.evidence_sleep_seen = false
	m.evidence_target = target
	m.evidence_mode = Evidence_mode(effective_mode(m.local_state))
	m.evidence_open = true
	m.evidence.Record(ctx, api.Connection_observation{Client_id: m.local_state.Client_id, Node_id: target.Node_id, Protocol: target.Protocol, Mode: m.evidence_mode, Session_id: m.evidence_session_id, Kind: "connection_established", Reason: "verified"})
}
func (m *Manager) end_observation(ctx context.Context, reason string, lost bool) {
	if m.evidence == nil || !m.evidence_open {
		return
	}
	m.evidence_open = false
	event := api.Connection_observation{Client_id: m.local_state.Client_id, Node_id: m.evidence_target.Node_id, Protocol: m.evidence_target.Protocol, Mode: m.evidence_mode, Session_id: m.evidence_session_id, Reason: reason}
	if lost {
		event.Kind = "connection_lost"
		m.evidence.Record(ctx, event)
	}
	event.Kind = "session_ended"
	if seconds := m.now().Sub(m.evidence_started).Seconds(); seconds >= 0 {
		event.Duration_seconds = &seconds
	}
	m.evidence.Record(ctx, event)
}

// Network invalidation itself is not a vote against a protocol.
func (m *Manager) Evidence_network_changed(ctx context.Context) {
	if m.evidence != nil && m.evidence.Network_changed(ctx) {
		m.op_mu.Lock()
		defer m.op_mu.Unlock()
		if m.evidence_open {
			m.evidence.Record(ctx, api.Connection_observation{Client_id: m.local_state.Client_id, Node_id: m.evidence_target.Node_id, Protocol: m.evidence_target.Protocol, Mode: m.evidence_mode, Session_id: m.evidence_session_id, Kind: "network_changed", Reason: "network_changed"})
		}
	}
}
func (m *Manager) Run_evidence(ctx context.Context) {
	if m.evidence != nil {
		m.evidence.Run(ctx)
	}
}
func (m *Manager) loss_observation_reason(now time.Time) string {
	if m.evidence_sleep_seen || (!m.evidence_last_tick.IsZero() && now.Sub(m.evidence_last_tick) > 30*time.Second) {
		return "unknown"
	}
	if m.protocol_handle != nil && !m.protocol_handle.Status().Running {
		return "tunnel_stopped"
	}
	if m.evidence_loss_reason != "" {
		return m.evidence_loss_reason
	}
	return "unknown"
}
