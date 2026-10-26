package diagnostics

import (
	"context"
	"errors"
	"time"
)

// diagnostics is the concrete Diagnostics: one Prober, and the node and
// protocol identity that a bare Sample does not carry.
type diagnostics struct {
	prober   Prober
	node_id  string
	protocol string
}

// New builds a Diagnostics that probes through prober and labels every
// Result with node_id and protocol.
func New(prober Prober, node_id, protocol string) Diagnostics {
	return &diagnostics{prober: prober, node_id: node_id, protocol: protocol}
}

// Test runs one probe and reports it. A failed probe returns an error and
// no Result -- never a Result of zeros with a nil error, and never a Result
// half filled from a partial probe. A zero RTT and a zero packet loss read
// as a perfect connection, which is the most dangerous lie this package
// could tell: either a real measurement comes back, or nothing does.
func (d *diagnostics) Test(ctx context.Context) (Result, error) {
	sample, err := d.prober.Probe(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Node_id:     d.node_id,
		Protocol:    d.protocol,
		Rtt:         sample.Rtt,
		Packet_loss: sample.Packet_loss,
		Dns_ok:      sample.Dns_ok,
		Internet_ok: sample.Internet_ok,
		Vpn_ok:      sample.Tunnel_up && sample.Internet_ok,
		Tested_at:   sample.Taken_at,
	}, nil
}

// Err_tunnel_down is what Verify returns when the tunnel itself never came
// up.
var Err_tunnel_down = errors.New("diagnostics: the tunnel is not carrying traffic")

// Err_no_internet is what Verify returns when the tunnel is up but nothing
// is reachable through it.
var Err_no_internet = errors.New("diagnostics: the tunnel is up but nothing is reachable through it")

// Verify checks that a protocol process which started is actually a
// connection: it requires Tunnel_up and Internet_ok, and names which one
// failed. DNS is reported but never required here -- a person with a broken
// resolver still has a working tunnel, and refusing to call that connected
// would turn a usable connection into a refusal.
func Verify(ctx context.Context, prober Prober) error {
	sample, err := prober.Probe(ctx)
	if err != nil {
		return err
	}
	if !sample.Tunnel_up {
		return Err_tunnel_down
	}
	if !sample.Internet_ok {
		return Err_no_internet
	}
	return nil
}

// Baseline_samples is how many probes of one session are averaged to form
// that session's baseline.
const Baseline_samples = 5

// Baseline is the mean RTT of the first Baseline_samples probes of one
// session, frozen once complete. It belongs to that session alone: carrying
// it across a switch would compare a node in Germany against a node in
// Singapore and call the second one degraded for being further away.
type Baseline struct {
	target string
	count  int
	total  time.Duration
	rtt    time.Duration
	ready  bool
}

// Observe folds one more sample into the baseline. Samples after the
// baseline is ready are ignored -- the baseline is frozen, not a moving
// average. A different measurement target starts a fresh baseline.
func (b *Baseline) Observe(sample Sample) {
	if b.target != sample.Measurement_target {
		*b = Baseline{target: sample.Measurement_target}
	}
	// A sample whose every attempt had to be resent measured no round
	// trip; a zero here would lower the yardstick for the whole session.
	if b.ready || sample.Rtt <= 0 {
		return
	}
	b.count++
	b.total += sample.Rtt
	if b.count >= Baseline_samples {
		b.rtt = b.total / time.Duration(b.count)
		b.ready = true
	}
}

// Ready reports whether the baseline has seen enough samples to be used.
func (b *Baseline) Ready() bool {
	return b.ready
}

// Rtt returns the frozen baseline RTT, and whether it is ready yet.
func (b *Baseline) Rtt() (time.Duration, bool) {
	if !b.ready {
		return 0, false
	}
	return b.rtt, true
}

// Thresholds is what Judge compares a sample against.
type Thresholds struct {
	Packet_loss        float64 // 0.05
	Latency_multiplier float64 // 2.0
}

// Default_thresholds returns the values CLIENT_AUTO_SWITCH.md section 5.2
// documents.
func Default_thresholds() Thresholds {
	return Thresholds{
		Packet_loss:        0.05,
		Latency_multiplier: 2.0,
	}
}

// Verdict is what Judge concluded about one sample.
type Verdict int

const (
	Verdict_healthy Verdict = iota
	Verdict_degraded
	Verdict_lost
)

// Judge compares one sample against a session's baseline and thresholds.
//
// Verdict_lost outranks everything else: a tunnel that is down, or through
// which nothing is reachable, is not slow, it is gone. Verdict_degraded
// follows when packet loss is above the threshold, or the baseline is ready
// and the RTT is above Latency_multiplier times it. Until the baseline is
// ready, latency never makes a verdict degraded -- otherwise the first
// probe of an honestly distant node would switch away from it, which is the
// flapping this whole design exists to avoid. Packet loss needs no
// yardstick, so it is judged from the first sample. Anything left is
// Verdict_healthy.
func Judge(sample Sample, baseline Baseline, thresholds Thresholds) Verdict {
	if !sample.Tunnel_up || !sample.Internet_ok {
		return Verdict_lost
	}
	detail := Degradation_of(sample, baseline, thresholds)
	if detail.Failed_probes || detail.Resent_probes || detail.Udp_lost || detail.High_latency {
		return Verdict_degraded
	}
	return Verdict_healthy
}
