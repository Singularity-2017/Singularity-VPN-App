package platform

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"singularity-vpn-app/internal/diagnostics"
)

// Default_attempts is how many round trips one probe makes. Four is the
// smallest number that makes a loss figure mean anything at all, and it
// keeps the cost honest: this runs every connection.Config.Monitor_interval,
// which is five seconds.
const Default_attempts = 4

// Default_attempt_timeout bounds one round trip. Two seconds, so that a
// whole probe -- the attempts run together, not one after another --
// finishes well inside the five-second monitoring interval. A probe that
// outlived its interval would queue behind the next tick on the manager's
// operation lock.
const Default_attempt_timeout = 2 * time.Second

// Network_prober is a diagnostics.Prober that measures a real connection: it
// asks the running protocol whether the tunnel is up, resolves a name, and
// opens Attempts TCP connections to a target, timing the ones that succeed.
//
// This prober returns a diagnostics.Sample only when every field in it is a
// real measurement. In every other case it returns an error.
//
// The reason is connection.Manager.Tick: it folds Sample.Rtt into the
// session's baseline before it judges anything, and that baseline is frozen
// for the rest of the session the moment it is complete. A Sample carrying a
// zero RTT because nothing could actually be measured would not just mislead
// one verdict -- it would permanently lower the yardstick every later
// verdict is compared against, and the connection would read as degraded for
// the rest of its life. Either a real measurement comes back, or nothing
// does.
//
// The two errors this type returns -- diagnostics.Err_tunnel_down and
// diagnostics.Err_no_internet -- are not invented here: they are exactly the
// two diagnostics.Verify itself returns when it does its own probing, so
// that a caller of Verify sees the same behaviour whether the failure was
// noticed inside Verify or inside this prober.
type Network_prober struct {
	tunnel_up   func() bool
	dns_name    string
	dial_target string

	attempts        int
	attempt_timeout time.Duration

	dial   func(ctx context.Context, network, address string) (net.Conn, error)
	lookup func(ctx context.Context, host string) ([]string, error)
	now    func() time.Time
}

// Prober_config is what New_network_prober needs to build a Network_prober.
type Prober_config struct {
	// Tunnel_up answers whether the tunnel is carrying anything at all. In
	// the service this is the protocol adapter's Status().Running.
	Tunnel_up func() bool

	// Dns_name is the name resolved to measure Dns_ok. Required.
	Dns_name string

	// Dial_target is the host:port opened to measure round trips. Required.
	Dial_target string

	// Attempts is how many round trips one probe makes. Zero means
	// Default_attempts.
	Attempts int

	// Attempt_timeout bounds one round trip. Zero means
	// Default_attempt_timeout.
	Attempt_timeout time.Duration

	// Dial and Lookup are the two impure edges, injectable so that every
	// decision in this file is tested without a network. Nil means the
	// real ones.
	Dial   func(ctx context.Context, network, address string) (net.Conn, error)
	Lookup func(ctx context.Context, host string) ([]string, error)

	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// New_network_prober builds a Network_prober, refusing a Dependencies
// missing anything it cannot invent. A prober that silently assumed a
// default target would be measuring the wrong thing while looking like it
// worked.
func New_network_prober(config Prober_config) (*Network_prober, error) {
	switch {
	case config.Tunnel_up == nil:
		return nil, fmt.Errorf("platform: Prober_config.Tunnel_up is required")
	case config.Dns_name == "":
		return nil, fmt.Errorf("platform: Prober_config.Dns_name is required")
	case config.Dial_target == "":
		return nil, fmt.Errorf("platform: Prober_config.Dial_target is required")
	}

	attempts := config.Attempts
	if attempts <= 0 {
		attempts = Default_attempts
	}
	attempt_timeout := config.Attempt_timeout
	if attempt_timeout <= 0 {
		attempt_timeout = Default_attempt_timeout
	}
	dial := config.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	lookup := config.Lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &Network_prober{
		tunnel_up:       config.Tunnel_up,
		dns_name:        config.Dns_name,
		dial_target:     config.Dial_target,
		attempts:        attempts,
		attempt_timeout: attempt_timeout,
		dial:            dial,
		lookup:          lookup,
		now:             now,
	}, nil
}

// Probe measures one sample of the current connection. See the comment on
// Network_prober for what it returns and why.
func (p *Network_prober) Probe(ctx context.Context) (diagnostics.Sample, error) {
	if !p.tunnel_up() {
		// Nothing below this can be measured through a tunnel that is not
		// there, and spending a whole probe's worth of dials to discover
		// that would only delay the verdict connection.Manager.Tick is
		// waiting for.
		return diagnostics.Sample{}, diagnostics.Err_tunnel_down
	}

	// A failed lookup is not fatal: diagnostics.Verify treats DNS as
	// informational, because a person with a broken resolver still has a
	// working tunnel, and turning that into a refusal here would make a
	// usable connection look like a dead one.
	addresses, lookup_err := p.lookup(ctx, p.dns_name)
	dns_ok := lookup_err == nil && len(addresses) > 0

	rtts, failed := p.dial_all(ctx)
	if len(rtts) == 0 {
		// A probe that was cancelled says nothing about the tunnel. Every
		// attempt fails the moment ctx ends, which looks exactly like a
		// tunnel that reaches nothing -- and reporting it as one would
		// have connection.Manager.Tick counting probe errors, and
		// eventually switching a person's node, because the service was
		// asked to shut down.
		if err := ctx.Err(); err != nil {
			return diagnostics.Sample{}, err
		}
		// There is no round trip to report, and a zero one would poison
		// the session's baseline for the rest of its life.
		return diagnostics.Sample{}, diagnostics.Err_no_internet
	}

	return diagnostics.Sample{
		Rtt: mean_rtt(rtts),

		// Packet_loss here is the fraction of TCP connection attempts
		// that did not complete inside Attempt_timeout -- not ICMP packet
		// loss, which is what CLIENT_AUTO_SWITCH.md section 5.2 had in
		// mind when it set the 5% threshold. A lost SYN inside a
		// two-second window counts as one whole failed attempt here,
		// where ICMP would have counted one lost packet: this signal is
		// coarser and more pessimistic than the one the threshold was
		// written for. With Default_attempts == 4, a single failed
		// attempt is already 25%, comfortably past the threshold, which
		// looks like a bug until it is followed to where it lands:
		// connection.Manager.Tick never switches anything on a degraded
		// verdict by itself -- Config.Recovery.Degradation_window
		// requires the connection to still be degraded fifteen seconds
		// later before recovery is even consulted, so a single blip
		// recovers on the very next tick.
		Packet_loss: float64(failed) / float64(p.attempts),

		Dns_ok: dns_ok,

		// Internet_ok and Tunnel_up are true in every Sample this prober
		// returns, because every case where either would be false --
		// Tunnel_up() reporting down, or every dial failing -- returns an
		// error above instead of a Sample. Both fields stay part of
		// diagnostics.Sample regardless, because diagnostics.Judge is
		// written against the type and not against this prober: a later
		// prober able to confirm reachability without measuring latency
		// will set them independently of Rtt.
		Internet_ok: true,
		Tunnel_up:   true,

		Taken_at: p.now(),
	}, nil
}

// dial_all runs Attempts dials concurrently, each bounded by its own
// Attempt_timeout, and returns the round trip of every one that succeeded
// plus a count of the ones that did not. Every connection that opens is
// closed here, including one that opens after another attempt has already
// answered the question this probe was asked -- a leaked handle in a
// service meant to run for months is a slow death.
func (p *Network_prober) dial_all(ctx context.Context) ([]time.Duration, int) {
	type outcome struct {
		rtt time.Duration
		ok  bool
	}
	outcomes := make([]outcome, p.attempts)

	var wg sync.WaitGroup
	wg.Add(p.attempts)
	for i := 0; i < p.attempts; i++ {
		go func(i int) {
			defer wg.Done()
			rtt, ok := p.dial_one(ctx)
			outcomes[i] = outcome{rtt: rtt, ok: ok}
		}(i)
	}
	wg.Wait()

	rtts := make([]time.Duration, 0, p.attempts)
	failed := 0
	for _, o := range outcomes {
		if o.ok {
			rtts = append(rtts, o.rtt)
		} else {
			failed++
		}
	}
	return rtts, failed
}

// dial_one performs one round trip: dial p.dial_target within its own
// Attempt_timeout, timed from just before the dial to just after it
// returns, and close the connection the moment it is no longer needed.
func (p *Network_prober) dial_one(ctx context.Context) (time.Duration, bool) {
	attempt_ctx := ctx
	if p.attempt_timeout > 0 {
		var cancel context.CancelFunc
		attempt_ctx, cancel = context.WithTimeout(ctx, p.attempt_timeout)
		defer cancel()
	}

	start := p.now()
	conn, err := p.dial(attempt_ctx, "tcp", p.dial_target)
	elapsed := p.now().Sub(start)
	if err != nil {
		return 0, false
	}

	_ = conn.Close()
	return elapsed, true
}

// mean_rtt averages the round trips that succeeded. It is its own function
// because it is the only arithmetic here, and arithmetic is worth testing
// exactly -- which cannot be done through Probe while several attempts
// share one clock and interleave.
func mean_rtt(rtts []time.Duration) time.Duration {
	var total time.Duration
	for _, rtt := range rtts {
		total += rtt
	}
	return total / time.Duration(len(rtts))
}
