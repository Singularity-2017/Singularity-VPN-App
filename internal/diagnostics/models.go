package diagnostics

import (
	"context"
	"time"
)

// Sample is one raw measurement, as a Prober took it. It is deliberately
// closer to the network than Result: Judge and Baseline work on samples so
// that judging a connection never has to reach back through a Result for a
// field it did not carry.
type Sample struct {
	// Measurement_target identifies the endpoint used for this RTT and loss.
	Measurement_target string
	Rtt                time.Duration
	Packet_loss        float64 // 0..1
	// Resent is the fraction of attempts that connected only after TCP
	// sent their SYN again, because nothing answered it within the
	// system's initial retransmission timeout. A packet of theirs was
	// lost or held, so they are loss, not latency, and are not in Rtt; a
	// sample whose every attempt was resent has no Rtt at all.
	Resent float64 // 0..1
	// Udp_rtt and Udp_loss come from DNS queries sent over UDP to the
	// same server, the way game traffic travels: nothing resends a lost
	// datagram, so this is the loss a game sees. Udp_measured says they
	// were taken at all; Udp_rtt is zero when no query was answered.
	Udp_measured bool
	Udp_rtt      time.Duration
	Udp_loss     float64 // 0..1
	Dns_ok       bool
	Internet_ok  bool
	Tunnel_up    bool
	Taken_at     time.Time
}

// Prober is the one impure edge of this package: everything that actually
// touches the network or the operating system lives behind it. Stage 3
// implements it for real; this package only ever consumes it.
type Prober interface {
	Probe(ctx context.Context) (Sample, error)
}

// Tunnel_health is an optional, local-only check. It must not dial or resolve.
// Probers without this capability keep the fast monitoring cadence.
type Tunnel_health interface {
	Tunnel_up() bool
}

// Result is a Sample joined with the node and protocol it was measured on,
// for whoever logs or displays it.
type Result struct {
	Node_id     string
	Protocol    string
	Rtt         time.Duration
	Packet_loss float64
	Dns_ok      bool
	Internet_ok bool
	Vpn_ok      bool
	Tested_at   time.Time
}

// Diagnostics runs one probe and turns it into a Result.
type Diagnostics interface {
	Test(ctx context.Context) (Result, error)
}
