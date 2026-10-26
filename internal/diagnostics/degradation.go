package diagnostics

import "time"

// Degradation keeps the evidence behind the verdict, without endpoint addresses.
type Degradation struct {
	High_latency  bool
	Failed_probes bool
	Rtt           time.Duration
	Baseline_rtt  time.Duration
	Packet_loss   float64
	Resent_probes bool
	Resent        float64
	Udp_lost      bool
	Udp_loss      float64
}

func Degradation_of(sample Sample, baseline Baseline, thresholds Thresholds) Degradation {
	var result Degradation
	if !sample.Tunnel_up || !sample.Internet_ok {
		return result
	}
	result.Rtt, result.Packet_loss = sample.Rtt, sample.Packet_loss
	result.Failed_probes = sample.Packet_loss > thresholds.Packet_loss
	result.Resent = sample.Resent
	result.Resent_probes = sample.Resent > thresholds.Packet_loss
	if sample.Udp_measured {
		result.Udp_loss = sample.Udp_loss
		result.Udp_lost = sample.Udp_loss > thresholds.Packet_loss
	}
	if rtt, ready := baseline.Rtt(); ready && baseline.target == sample.Measurement_target && sample.Rtt > 0 {
		result.Baseline_rtt = rtt
		result.High_latency = sample.Rtt > time.Duration(float64(rtt)*thresholds.Latency_multiplier)
	}
	return result
}
