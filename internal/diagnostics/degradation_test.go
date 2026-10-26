package diagnostics

import (
	"testing"
	"time"
)

func Test_degradation_reports_each_actual_cause(t *testing.T) {
	var baseline Baseline
	for range Baseline_samples {
		baseline.Observe(Sample{Rtt: 20 * time.Millisecond, Measurement_target: "synthetic"})
	}
	for _, tc := range []struct {
		name            string
		rtt             time.Duration
		loss            float64
		target          string
		latency, probes bool
	}{
		{"latency", 100 * time.Millisecond, 0, "synthetic", true, false},
		{"probes", 20 * time.Millisecond, .25, "synthetic", false, true},
		{"both", 100 * time.Millisecond, .25, "synthetic", true, true},
		{"healthy", 20 * time.Millisecond, 0, "synthetic", false, false},
		{"different_target", 100 * time.Millisecond, 0, "other", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := Sample{Tunnel_up: true, Internet_ok: true, Rtt: tc.rtt, Packet_loss: tc.loss, Measurement_target: tc.target}
			got := Degradation_of(sample, baseline, Default_thresholds())
			if got.High_latency != tc.latency || got.Failed_probes != tc.probes {
				t.Fatalf("causes = %+v", got)
			}
			if (Judge(sample, baseline, Default_thresholds()) == Verdict_degraded) != (got.High_latency || got.Failed_probes) {
				t.Fatal("judgement and explanation disagree")
			}
		})
	}
}

func Test_resent_probes_are_loss_not_latency(t *testing.T) {
	var baseline Baseline
	for range Baseline_samples {
		baseline.Observe(Sample{Rtt: 56 * time.Millisecond, Measurement_target: "synthetic"})
	}
	// Every attempt connected only after a resent SYN: no round trip.
	sample := Sample{Resent: 1, Measurement_target: "synthetic", Tunnel_up: true, Internet_ok: true}
	detail := Degradation_of(sample, baseline, Default_thresholds())
	if !detail.Resent_probes || detail.High_latency || detail.Failed_probes || detail.Resent != 1 {
		t.Fatalf("detail = %+v, want resent loss alone", detail)
	}
	if Judge(sample, baseline, Default_thresholds()) != Verdict_degraded {
		t.Fatal("lost packets must still degrade: the check stays sensitive")
	}
	healthy := Sample{Rtt: 56 * time.Millisecond, Measurement_target: "synthetic", Tunnel_up: true, Internet_ok: true}
	if Judge(healthy, baseline, Default_thresholds()) != Verdict_healthy {
		t.Fatal("a prompt sample is healthy")
	}
}

func Test_a_sample_without_a_round_trip_does_not_lower_the_baseline(t *testing.T) {
	var baseline Baseline
	for i := range Baseline_samples {
		baseline.Observe(Sample{Rtt: 56 * time.Millisecond, Measurement_target: "synthetic"})
		if i == 0 {
			baseline.Observe(Sample{Resent: 1, Measurement_target: "synthetic"})
		}
	}
	if rtt, ready := baseline.Rtt(); !ready || rtt != 56*time.Millisecond {
		t.Fatalf("baseline = %v, %v; want 56ms from measured samples only", rtt, ready)
	}
}

func Test_lost_udp_degrades_even_when_tcp_is_clean(t *testing.T) {
	var baseline Baseline
	for range Baseline_samples {
		baseline.Observe(Sample{Rtt: 56 * time.Millisecond, Measurement_target: "synthetic"})
	}
	sample := Sample{Rtt: 56 * time.Millisecond, Udp_measured: true, Udp_rtt: 55 * time.Millisecond, Udp_loss: 1.0 / 8, Measurement_target: "synthetic", Tunnel_up: true, Internet_ok: true}
	detail := Degradation_of(sample, baseline, Default_thresholds())
	if !detail.Udp_lost || detail.Udp_loss != 1.0/8 || detail.Failed_probes || detail.Resent_probes || detail.High_latency {
		t.Fatalf("detail = %+v, want UDP loss alone", detail)
	}
	if Judge(sample, baseline, Default_thresholds()) != Verdict_degraded {
		t.Fatal("one lost datagram in eight is what a game feels; it must degrade")
	}
	sample.Udp_loss = 0
	if Judge(sample, baseline, Default_thresholds()) != Verdict_healthy {
		t.Fatal("every datagram answered is healthy")
	}
	sample.Udp_measured, sample.Udp_loss = false, 1
	if Degradation_of(sample, baseline, Default_thresholds()).Udp_lost {
		t.Fatal("UDP that was never measured cannot be lost")
	}
}
