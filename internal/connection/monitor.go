package connection

import (
	"context"
	"time"

	"singularity-vpn-app/internal/diagnostics"
)

// probe_schedule is used only by Run. Explicit Tick calls always measure.
// Each interval needs three healthy confirmations before it can grow.
type probe_schedule struct {
	next     time.Time
	interval time.Duration
	healthy  int
}

func (s *probe_schedule) observe(now time.Time, stable bool, fast, maximum time.Duration) {
	if s.interval == 0 || !stable {
		s.interval, s.healthy = fast, 0
	}
	if stable {
		s.healthy++
		if s.healthy >= 3 {
			s.interval = min(maximum, s.interval+min(s.interval, maximum-s.interval))
			s.healthy = 0
		}
	}
	s.next = now.Add(s.interval)
}

// monitor_tick never queues behind a user action. The next pulse can inspect
// the resulting session instead. Cheap health checks continue during backoff.
func (m *Manager) monitor_tick(ctx context.Context, now time.Time) {
	if !m.op_mu.TryLock() {
		return
	}
	defer m.op_mu.Unlock()
	current := m.get_state()
	if current != State_connected && current != State_degraded {
		return
	}
	health, has_health := m.prober.(diagnostics.Tunnel_health)
	if has_health && health.Tunnel_up() && now.Before(m.monitor.next) {
		// Skipping a full probe is intentional, not evidence of system sleep.
		m.record_monitor_tick(now)
		m.keep_companion(ctx, now)
		return
	}
	m.tick_locked(ctx, now)
	// Start the wait after slow work; no queued catch-up probes. A fresh
	// session may have reset the schedule while recovery ran inside Tick.
	if m.monitor.interval > 0 {
		m.monitor.next = m.now().Add(m.monitor.interval)
	}
}

func (m *Manager) record_monitor_tick(now time.Time) {
	if !m.evidence_last_tick.IsZero() && now.Sub(m.evidence_last_tick) > 30*time.Second {
		m.evidence_sleep_seen = true
	}
	m.evidence_last_tick = now
}

// Only the measurement is cancellable here, not a recovery already changing
// the adapter. A user switch/disconnect need not wait out DNS and TCP timeouts.
func (m *Manager) monitor_probe(ctx context.Context) (diagnostics.Sample, error) {
	probe_ctx, cancel := context.WithCancel(ctx)
	m.probe_mu.Lock()
	m.probe_cancel = cancel
	m.probe_mu.Unlock()
	defer func() {
		m.probe_mu.Lock()
		m.probe_cancel = nil
		m.probe_mu.Unlock()
		cancel()
	}()
	sample, err := m.prober.Probe(probe_ctx)
	if probe_ctx.Err() != nil {
		return diagnostics.Sample{}, probe_ctx.Err()
	}
	return sample, err
}

func (m *Manager) cancel_monitor_probe() {
	m.probe_mu.Lock()
	defer m.probe_mu.Unlock()
	if m.probe_cancel != nil {
		m.probe_cancel()
	}
}
