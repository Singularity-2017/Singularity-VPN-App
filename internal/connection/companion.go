package connection

import (
	"context"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/protocols"
)

// The AntiZapret companion. While games go through the tunnel alone and
// the person chose AntiZapret, the blocked destinations still need a tunnel
// of their own: an AntiZapret node drops everything outside its forward
// list, so the games cannot ride the AntiZapret profile, and the games-only
// tunnel carries nothing but the games. OpenVPN runs on an adapter of its
// own beside the WireGuard one the games use, and its AntiZapret profile
// pushes its own routes and resolver, so the two run side by side on the
// same node.

const companion_protocol = "openvpn"

// A companion that could not be brought up is left companion_retry_first
// before it is tried again, and each failure in a row doubles that, up to
// companion_retry_most. Every try brings an adapter up and takes it down,
// and on Windows each of those sets every program on the machine re-reading
// its adapters through the DNS Client service: tried every minute, one that
// would never come up kept that service spinning on most cores for as long
// as the boost lasted (2026-10-29).
const (
	companion_retry_first = time.Minute
	companion_retry_most  = 30 * time.Minute
)

type companion struct {
	handle    protocols.Protocol
	node      string
	failed_at time.Time
	// failures counts the tries in a row that did not come up, on
	// failed_node; a boost ended or another node starts the count afresh.
	failures    int
	failed_node string
}

// forget_failures starts the count afresh: a boost ended, or another node.
// A main tunnel only passing through a reconnect does not count as either.
func (c *companion) forget_failures() {
	c.failures, c.failed_at, c.failed_node = 0, time.Time{}, ""
}

// retry_wait is how long after the last failure the next try waits.
func (c *companion) retry_wait() time.Duration {
	wait := companion_retry_first
	for i := 1; i < c.failures && wait < companion_retry_most; i++ {
		wait *= 2
	}
	return min(wait, companion_retry_most)
}

// companion_wanted: games alone in the tunnel, and AntiZapret chosen for
// everything else.
func (m *Manager) companion_wanted() bool {
	return effective_mode(m.local_state) == Tunnel_games &&
		m.local_state.Tunnel_mode == Tunnel_antizapret &&
		m.current.Protocol != companion_protocol
}

// keep_companion brings the companion up, moves it with the node, or takes
// it down, to match what is wanted now. The caller holds op_mu. A failure
// is logged and tried again after retry_wait: the games tunnel is the one
// the person pressed for, and it does not wait on this one.
func (m *Manager) keep_companion(ctx context.Context, now time.Time) {
	state := m.get_state()
	want := (state == State_connected || state == State_degraded) && m.companion_wanted()
	c := &m.companion
	if c.handle != nil && (!want || c.node != m.current.Node_id || !c.handle.Status().Running) {
		if want {
			m.logger.Warn("the AntiZapret tunnel beside the games went down; bringing it back", "node_id", m.current.Node_id)
		}
		m.stop_companion(ctx)
	}
	if !m.companion_wanted() || c.failed_node != m.current.Node_id {
		c.forget_failures()
	}
	if !want || c.handle != nil {
		return
	}
	if c.failures > 0 && now.Sub(c.failed_at) < c.retry_wait() {
		return
	}
	fail := func(what string, err error) {
		c.failed_at, c.failed_node = now, m.current.Node_id
		c.failures++
		m.logger.Warn("AntiZapret beside the games: "+what, "node_id", m.current.Node_id, "err", err, "next_try_in", c.retry_wait())
	}
	protocol, err := m.protocols.Get(companion_protocol)
	if err != nil {
		fail("this build has no OpenVPN", err)
		return
	}
	token, err := m.token.Token(ctx)
	if err != nil {
		fail("no token", err)
		return
	}
	config, err := m.control_plane.Fetch_config(ctx, token, m.local_state.Client_id, companion_protocol, m.current.Node_id, api.Mode_antizapret)
	if err != nil {
		fail("its profile could not be had", err)
		return
	}
	connect_ctx := ctx
	if timeout := protocol_timeout(m.config.Protocol_timeout, protocol); timeout > 0 {
		var cancel context.CancelFunc
		connect_ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := protocol.Connect(connect_ctx, protocols.Connection_config{
		Node_id: m.current.Node_id, Protocol: companion_protocol, Data: []byte(config.Config),
	}); err != nil {
		fail("it did not come up", err)
		return
	}
	c.handle, c.node = protocol, m.current.Node_id
	c.forget_failures()
	m.logger.Info("AntiZapret runs beside the games", "node_id", c.node, "protocol", companion_protocol)
}

// stop_companion takes the companion down, if there is one.
func (m *Manager) stop_companion(ctx context.Context) {
	if m.companion.handle == nil {
		return
	}
	if err := m.companion.handle.Disconnect(ctx); err != nil {
		m.logger.Warn("taking the AntiZapret tunnel beside the games down", "err", err)
	}
	m.companion.handle = nil
}

// drop_unwanted_companion takes the companion down when it is no longer
// wanted. Bringing one up waits for the next tick: an OpenVPN connect can
// take many seconds, and the games tunnel the person pressed for does not
// wait on it.
func (m *Manager) drop_unwanted_companion(ctx context.Context) {
	state := m.get_state()
	if m.companion.handle != nil && !((state == State_connected || state == State_degraded) && m.companion_wanted()) {
		m.stop_companion(ctx)
	}
	if !m.companion_wanted() {
		m.companion.forget_failures()
	}
}

// yield_adapter gives the main tunnel the adapter the companion holds,
// when the main tunnel is about to connect over the same protocol.
func (m *Manager) yield_adapter(ctx context.Context, protocol protocols.Protocol) {
	if m.companion.handle != nil && protocol.Name() == companion_protocol {
		m.logger.Info("the main tunnel moves to OpenVPN; the AntiZapret tunnel beside the games gives way")
		m.stop_companion(ctx)
	}
}
