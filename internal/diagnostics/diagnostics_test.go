package diagnostics

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stub_prober returns a fixed sample and error, so tests can drive
// Diagnostics and Verify without a real network.
type stub_prober struct {
	sample Sample
	err    error
}

func (s stub_prober) Probe(ctx context.Context) (Sample, error) {
	return s.sample, s.err
}

var err_probe_failed = errors.New("probe: unreachable")

func Test_a_probe_error_produces_an_error_and_no_result(t *testing.T) {
	diagnostics := New(stub_prober{err: err_probe_failed}, "de-01", "wireguard")

	result, err := diagnostics.Test(context.Background())
	if err == nil {
		t.Fatal("Test() error = nil, want an error")
	}
	if result != (Result{}) {
		t.Fatalf("Test() result = %+v, want the zero value", result)
	}
}

func Test_result_carries_the_node_and_protocol_it_was_told(t *testing.T) {
	sample := Sample{
		Rtt:         20 * time.Millisecond,
		Packet_loss: 0,
		Dns_ok:      true,
		Internet_ok: true,
		Tunnel_up:   true,
		Taken_at:    time.Now(),
	}
	diagnostics := New(stub_prober{sample: sample}, "de-01", "wireguard")

	result, err := diagnostics.Test(context.Background())
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if result.Node_id != "de-01" || result.Protocol != "wireguard" {
		t.Fatalf("Test() result = %+v, want Node_id de-01 and Protocol wireguard", result)
	}
	if result.Rtt != sample.Rtt || result.Packet_loss != sample.Packet_loss {
		t.Fatalf("Test() result = %+v, did not carry the sample's measurements", result)
	}
	if !result.Vpn_ok {
		t.Fatalf("Test() result.Vpn_ok = false, want true for an up tunnel with internet")
	}
}

func Test_verify_accepts_an_up_tunnel_with_internet(t *testing.T) {
	prober := stub_prober{sample: Sample{Tunnel_up: true, Internet_ok: true}}

	if err := Verify(context.Background(), prober); err != nil {
		t.Fatalf("Verify() error = %v, want nil", err)
	}
}

func Test_verify_refuses_a_down_tunnel_with_err_tunnel_down(t *testing.T) {
	prober := stub_prober{sample: Sample{Tunnel_up: false, Internet_ok: true}}

	err := Verify(context.Background(), prober)
	if !errors.Is(err, Err_tunnel_down) {
		t.Fatalf("Verify() error = %v, want Err_tunnel_down", err)
	}
}

func Test_verify_refuses_an_up_tunnel_with_no_internet_with_err_no_internet(t *testing.T) {
	prober := stub_prober{sample: Sample{Tunnel_up: true, Internet_ok: false}}

	err := Verify(context.Background(), prober)
	if !errors.Is(err, Err_no_internet) {
		t.Fatalf("Verify() error = %v, want Err_no_internet", err)
	}
}

func Test_verify_accepts_a_tunnel_whose_dns_is_broken(t *testing.T) {
	prober := stub_prober{sample: Sample{Tunnel_up: true, Internet_ok: true, Dns_ok: false}}

	if err := Verify(context.Background(), prober); err != nil {
		t.Fatalf("Verify() error = %v, want nil: a broken resolver is not a broken tunnel", err)
	}
}

func Test_the_baseline_is_not_ready_before_baseline_samples(t *testing.T) {
	var baseline Baseline
	for i := 0; i < Baseline_samples-1; i++ {
		baseline.Observe(Sample{Rtt: 20 * time.Millisecond})
	}

	if baseline.Ready() {
		t.Fatal("Ready() = true before Baseline_samples observations")
	}
	if _, ok := baseline.Rtt(); ok {
		t.Fatal("Rtt() ok = true before the baseline is ready")
	}
}

func Test_the_baseline_is_frozen_after_baseline_samples_and_a_later_slow_sample_does_not_move_it(t *testing.T) {
	var baseline Baseline
	for i := 0; i < Baseline_samples; i++ {
		baseline.Observe(Sample{Rtt: 20 * time.Millisecond})
	}
	if !baseline.Ready() {
		t.Fatal("Ready() = false after Baseline_samples observations")
	}
	frozen, ok := baseline.Rtt()
	if !ok {
		t.Fatal("Rtt() ok = false right after the baseline became ready")
	}
	if frozen != 20*time.Millisecond {
		t.Fatalf("Rtt() = %v, want 20ms", frozen)
	}

	baseline.Observe(Sample{Rtt: time.Second})

	after, ok := baseline.Rtt()
	if !ok || after != frozen {
		t.Fatalf("Rtt() = %v, %v, want the frozen baseline %v unchanged", after, ok, frozen)
	}
}

func Test_high_latency_with_no_baseline_is_healthy(t *testing.T) {
	var baseline Baseline
	sample := Sample{Tunnel_up: true, Internet_ok: true, Rtt: 5 * time.Second}

	got := Judge(sample, baseline, Default_thresholds())
	if got != Verdict_healthy {
		t.Fatalf("Judge() = %v, want Verdict_healthy before the baseline is ready", got)
	}
}

func Test_high_latency_with_a_baseline_is_degraded(t *testing.T) {
	var baseline Baseline
	for i := 0; i < Baseline_samples; i++ {
		baseline.Observe(Sample{Rtt: 20 * time.Millisecond})
	}
	sample := Sample{Tunnel_up: true, Internet_ok: true, Rtt: 100 * time.Millisecond}

	got := Judge(sample, baseline, Default_thresholds())
	if got != Verdict_degraded {
		t.Fatalf("Judge() = %v, want Verdict_degraded for RTT far above the baseline", got)
	}
}

func Test_loss_above_the_threshold_is_degraded_from_the_first_sample(t *testing.T) {
	var baseline Baseline
	sample := Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}

	got := Judge(sample, baseline, Default_thresholds())
	if got != Verdict_degraded {
		t.Fatalf("Judge() = %v, want Verdict_degraded: packet loss needs no baseline", got)
	}
}

func Test_a_down_tunnel_is_lost_not_degraded_even_when_it_is_also_slow(t *testing.T) {
	var baseline Baseline
	for i := 0; i < Baseline_samples; i++ {
		baseline.Observe(Sample{Rtt: 20 * time.Millisecond})
	}
	sample := Sample{Tunnel_up: false, Internet_ok: false, Rtt: 5 * time.Second, Packet_loss: 0.9}

	got := Judge(sample, baseline, Default_thresholds())
	if got != Verdict_lost {
		t.Fatalf("Judge() = %v, want Verdict_lost: a down tunnel outranks degraded", got)
	}
}

func Test_target_changes_never_compare_or_mix_endpoint_baselines(t *testing.T) {
	var baseline Baseline
	primary := Sample{Measurement_target: "primary", Rtt: 10 * time.Millisecond, Internet_ok: true, Tunnel_up: true}
	fallback := Sample{Measurement_target: "fallback", Rtt: 100 * time.Millisecond, Internet_ok: true, Tunnel_up: true}
	for i := 0; i < Baseline_samples; i++ {
		baseline.Observe(primary)
	}
	if Judge(fallback, baseline, Default_thresholds()) != Verdict_healthy {
		t.Fatal("fallback compared to primary before observation")
	}
	baseline.Observe(fallback)
	if baseline.Ready() {
		t.Fatal("primary baseline retained after fallback")
	}
	if Judge(fallback, baseline, Default_thresholds()) != Verdict_healthy {
		t.Fatal("healthy fallback degraded")
	}
	for i := 1; i < Baseline_samples; i++ {
		baseline.Observe(fallback)
	}
	if got, _ := baseline.Rtt(); got != fallback.Rtt {
		t.Fatalf("fallback baseline contaminated: %v", got)
	}
	baseline.Observe(primary)
	if baseline.Ready() {
		t.Fatal("fallback baseline retained after primary returns")
	}
	for i := 1; i < Baseline_samples; i++ {
		baseline.Observe(primary)
	}
	if got, _ := baseline.Rtt(); got != primary.Rtt {
		t.Fatalf("primary baseline contaminated: %v", got)
	}
	legacy := primary
	legacy.Measurement_target = ""
	baseline.Observe(legacy)
	if baseline.Ready() {
		t.Fatal("empty target must also start a separate baseline")
	}
}
