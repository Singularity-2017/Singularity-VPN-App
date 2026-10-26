package connection

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/recovery"
)

type health_prober struct {
	*fake_prober
	up     bool
	checks int
}

func (p *health_prober) Tunnel_up() bool {
	p.checks++
	return p.up
}

func Test_probe_schedule_confirms_stability_and_caps_backoff(t *testing.T) {
	var schedule probe_schedule
	for i, want := range []time.Duration{5, 5, 10, 10, 10, 20, 20, 20, 40, 40, 40, 60, 60, 60, 60} {
		schedule.observe(epoch, true, 5*time.Second, time.Minute)
		if schedule.interval != want*time.Second {
			t.Fatalf("sample %d interval = %v, want %vs", i, schedule.interval, want)
		}
	}
	schedule.observe(epoch, false, 5*time.Second, time.Minute)
	if schedule.interval != 5*time.Second || schedule.healthy != 0 {
		t.Fatalf("failure retained backoff: %+v", schedule)
	}
}

func Test_monitor_backoff_keeps_health_fast_and_manual_tick_unconditional(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{}); err != nil {
		t.Fatal(err)
	}
	prober := &health_prober{fake_prober: env.prober, up: true}
	manager.prober = prober
	env.prober.set([]prober_response{{sample: healthy_sample}})
	for i := 0; i < 100; i++ {
		manager.monitor_tick(context.Background(), env.clock.advance(5*time.Second))
	}
	if prober.checks != 100 || prober.calls >= 35 || manager.monitor.interval != time.Minute {
		t.Fatalf("checks=%d probes=%d interval=%v", prober.checks, prober.calls, manager.monitor.interval)
	}
	if manager.evidence_sleep_seen {
		t.Fatal("intentional backoff was recorded as sleep")
	}
	before := prober.calls
	manager.Tick(context.Background(), env.clock.Now())
	manager.Tick(context.Background(), env.clock.Now())
	if prober.calls != before+2 {
		t.Fatal("explicit Tick was suppressed by backoff")
	}
	// Changing the network takes a fresh sample immediately and rebuilds its baseline.
	manager.Network_changed(context.Background(), env.clock.Now())
	if prober.calls != before+3 || manager.monitor.interval != 5*time.Second || manager.baseline.Ready() {
		t.Fatal("network change retained a stable schedule or baseline")
	}
	manager.monitor = probe_schedule{interval: time.Minute, next: env.clock.Now().Add(time.Minute)}
	if err := manager.Switch(context.Background(), recovery.Target{Node_id: "de-01", Protocol: "wireguard"}); err != nil {
		t.Fatal(err)
	}
	if !manager.monitor.next.IsZero() {
		t.Fatal("new session retained the old schedule")
	}
}

func Test_monitor_without_local_health_never_skips_probes(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{}); err != nil {
		t.Fatal(err)
	}
	before := env.prober.calls
	manager.monitor.next = epoch.Add(time.Hour)
	manager.monitor_tick(context.Background(), env.clock.advance(5*time.Second))
	if env.prober.calls != before+1 {
		t.Fatal("probe skipped without a local health check")
	}
}

func Test_monitor_down_tunnel_bypasses_backoff_and_failure_restores_fast_schedule(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{}); err != nil {
		t.Fatal(err)
	}
	manager.prober = &health_prober{fake_prober: env.prober}
	env.prober.set([]prober_response{{err: diagnostics.Err_tunnel_down}})
	manager.monitor = probe_schedule{interval: time.Minute, next: epoch.Add(time.Minute)}
	manager.monitor_tick(context.Background(), env.clock.advance(5*time.Second))
	if env.prober.calls != 1 || manager.consecutive_probe_errors != 1 || manager.monitor.interval != 5*time.Second {
		t.Fatal("local tunnel failure waited for the full-probe interval")
	}
	manager.monitor_tick(context.Background(), env.clock.advance(5*time.Second))
	if env.protocol.disconnect_calls == 0 {
		t.Fatal("second failure did not reach recovery at the fast cadence")
	}
}

func Test_monitor_one_bad_sample_is_rechecked_before_recovery(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatal(err)
	}
	manager.prober = &health_prober{fake_prober: env.prober, up: true}
	bad := healthy_sample
	bad.Packet_loss = 0.25
	env.prober.set([]prober_response{{sample: bad}, {sample: healthy_sample}})
	manager.monitor = probe_schedule{interval: time.Minute, next: epoch.Add(time.Minute)}
	manager.monitor_tick(context.Background(), env.clock.advance(time.Minute))
	if manager.State() != State_connected || manager.monitor.interval != 5*time.Second || env.protocol.disconnect_calls != 0 {
		t.Fatal("one bad sample degraded the connection, escalated or retained the slow schedule")
	}
	manager.monitor_tick(context.Background(), env.clock.advance(5*time.Second))
	if manager.State() != State_connected || env.prober.calls != 2 || env.protocol.disconnect_calls != 0 {
		t.Fatal("healthy confirmation did not clear degradation without recovery")
	}
}

type cancelled_measurement struct {
	started chan struct{}
	calls   atomic.Int32
}

func (p *cancelled_measurement) Probe(ctx context.Context) (diagnostics.Sample, error) {
	if p.calls.Add(1) == 1 {
		close(p.started)
		<-ctx.Done()
		// Even a prober returning a sample after cancellation must be ignored.
	}
	return healthy_sample, nil
}

func Test_switch_cancels_pending_probe_and_status_does_not_wait_for_it(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{}); err != nil {
		t.Fatal(err)
	}
	prober := &cancelled_measurement{started: make(chan struct{})}
	manager.prober = prober
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tick_done := make(chan struct{})
	go func() { manager.Tick(ctx, epoch); close(tick_done) }()
	select {
	case <-prober.started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	status_done := make(chan Status_snapshot, 1)
	go func() { status_done <- manager.Status_snapshot() }()
	select {
	case snapshot := <-status_done:
		if snapshot.State != State_connected {
			t.Fatal(snapshot.State)
		}
	case <-time.After(time.Second):
		t.Fatal("status queued behind the probe")
	}
	switch_done := make(chan error, 1)
	go func() { switch_done <- manager.Switch(ctx, recovery.Target{Node_id: "de-01", Protocol: "wireguard"}) }()
	select {
	case err := <-switch_done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("switch did not cancel the pending measurement")
	}
	<-tick_done
	if manager.consecutive_probe_errors != 0 {
		t.Fatal("cancelled measurement counted as network failure")
	}
}

func Test_monitor_run_can_stop_while_operation_lock_is_held(t *testing.T) {
	env := new_test_env(t)
	env.config.Monitor_interval = time.Millisecond
	manager := env.build()
	manager.op_mu.Lock()
	defer manager.op_mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor Run queued behind an operation")
	}
}
