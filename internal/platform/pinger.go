package platform

import (
	"context"
	"net/netip"
	"time"
)

// Pinger measures the round trip to one address with a single ICMP echo.
//
// Ping returns a duration only for a real reply from destination: a timeout,
// an unreachable answer, a reply from some other address and an address the
// implementation cannot probe (the Windows one is IPv4 only) are all errors,
// never a zero duration. The caller shows the figure to a person, and a
// number on the screen is either a measurement or it is absent.
//
// The echo must not ride the VPN tunnel, even while one is up: the point is
// the latency to that address from this machine, and a probe that went
// through the current server would measure the current server for every
// address asked about.
//
// Ping honours ctx's deadline as a bound on the wait. It may not be able to
// return the moment ctx is cancelled, but it does not outlive the deadline by
// more than a call's own granularity.
type Pinger interface {
	Ping(ctx context.Context, destination netip.Addr) (time.Duration, error)
}
