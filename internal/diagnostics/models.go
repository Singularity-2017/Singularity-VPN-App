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
	Rtt         time.Duration
	Packet_loss float64 // 0..1
	Dns_ok      bool
	Internet_ok bool
	Tunnel_up   bool
	Taken_at    time.Time
}

// Prober is the one impure edge of this package: everything that actually
// touches the network or the operating system lives behind it. Stage 3
// implements it for real; this package only ever consumes it.
type Prober interface {
	Probe(ctx context.Context) (Sample, error)
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
