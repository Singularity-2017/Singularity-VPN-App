package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"singularity-vpn-app/internal/diagnostics"
)

// Default_attempts is how many round trips one probe makes. Four is the
// smallest number that makes a loss figure mean anything at all, and it
// keeps the cost bounded when adaptive monitoring requests a sample.
const Default_attempts = 4

// Default_attempt_timeout bounds one round trip. Two seconds, so that a
// whole probe -- the attempts run together, not one after another --
// finishes well inside the five-second monitoring interval. A probe that
// outlived its interval would queue behind the next tick on the manager's
// operation lock.
const Default_attempt_timeout = 2 * time.Second

// Default_udp_queries is how many DNS queries one probe sends over UDP, the
// way game traffic travels: a lost datagram is gone, nothing resends it.
// Eight spaced a little apart see loss a burst of four dials would miss.
const Default_udp_queries = 8

// udp_query_spacing and udp_timeout shape the UDP burst: the queries go
// out over 140 ms, and an answer later than a second is as good as lost
// to a game.
const (
	udp_query_spacing = 20 * time.Millisecond
	udp_timeout       = time.Second
)

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
// does. The one sample without a round trip is the one whose every attempt
// connected only after a resent SYN: its loss is real, it says so in
// Resent, and the baseline skips its absent Rtt.
//
// The two errors this type returns -- diagnostics.Err_tunnel_down and
// diagnostics.Err_no_internet -- are not invented here: they are exactly the
// two diagnostics.Verify itself returns when it does its own probing, so
// that a caller of Verify sees the same behaviour whether the failure was
// noticed inside Verify or inside this prober.
type Network_prober struct {
	log_mu           sync.Mutex
	target_logs      map[string]probe_log_state
	tunnel_up        func() bool
	dns_name         string
	dial_target      string
	target_now       func() string
	fallback_target  string
	fallback_allowed func() bool
	logger           *slog.Logger

	attempts        int
	attempt_timeout time.Duration
	udp_queries     int
	// udp_dns: the UDP answers stand for the DNS check, and the system
	// resolver is asked only when no query could be sent at all.
	udp_dns bool

	dial     func(ctx context.Context, network, address string) (net.Conn, error)
	dial_udp func(ctx context.Context, network, address string) (net.Conn, error)
	lookup   func(ctx context.Context, host string) ([]string, error)
	now      func() time.Time
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

	// Dial_target_now, when set, is asked at the start of every probe, and
	// a non-empty answer is dialed instead of Dial_target for that probe.
	// A tunnel that carries only part of the traffic drops the static
	// target; this lets the service name one the live tunnel does carry.
	Dial_target_now func() string

	// Fallback_target corroborates total failure only when Fallback_allowed confirms full routing.
	Fallback_target  string
	Fallback_allowed func() bool
	Logger           *slog.Logger

	// Attempts is how many round trips one probe makes. Zero means
	// Default_attempts.
	Attempts int

	// Attempt_timeout bounds one round trip. Zero means
	// Default_attempt_timeout.
	Attempt_timeout time.Duration

	// Udp_queries is how many DNS queries for Dns_name each probe sends
	// over UDP to port 53 of the host it dials -- every probe target is a
	// resolver. Zero sends none. With it set and Lookup nil, Dns_ok comes
	// from these answers, and the system resolver is never asked: on
	// Windows every lookup through it makes the DNS Client service read
	// the configuration of every interface again.
	Udp_queries int

	// Dial, Dial_udp and Lookup are the impure edges, injectable so that
	// every decision in this file is tested without a network. Nil means
	// the real ones.
	Dial     func(ctx context.Context, network, address string) (net.Conn, error)
	Dial_udp func(ctx context.Context, network, address string) (net.Conn, error)
	Lookup   func(ctx context.Context, host string) ([]string, error)

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
	dial_udp := config.Dial_udp
	if dial_udp == nil {
		dial_udp = (&net.Dialer{}).DialContext
	}
	lookup := config.Lookup
	udp_dns := lookup == nil && config.Udp_queries > 0
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	if config.Logger == nil {
		config.Logger = slog.New(slog.DiscardHandler)
	}
	return &Network_prober{
		tunnel_up:        config.Tunnel_up,
		dns_name:         config.Dns_name,
		dial_target:      config.Dial_target,
		target_now:       config.Dial_target_now,
		fallback_target:  config.Fallback_target,
		fallback_allowed: config.Fallback_allowed,
		logger:           config.Logger,
		attempts:         attempts,
		attempt_timeout:  attempt_timeout,
		udp_queries:      max(config.Udp_queries, 0),
		udp_dns:          udp_dns,
		dial:             dial,
		dial_udp:         dial_udp,
		lookup:           lookup,
		now:              now,
	}, nil
}

// Probe measures one sample of the current connection. See the comment on
// Network_prober for what it returns and why.
func (p *Network_prober) Tunnel_up() bool {
	return p.tunnel_up()
}

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
	// usable connection look like a dead one. Bound it by attempt_timeout
	// so a stalled resolver cannot hold up the TCP reachability check.
	target := p.dial_target
	if p.target_now != nil {
		if current := p.target_now(); current != "" {
			target = current
		}
	}

	// The UDP burst goes out alongside the dials, to the same server.
	var udp udp_outcome
	udp_done := make(chan struct{})
	go func() {
		defer close(udp_done)
		udp = p.udp_probe(ctx, target)
	}()

	system_lookup := func() bool {
		lookup_ctx := ctx
		if p.attempt_timeout > 0 {
			var cancel context.CancelFunc
			lookup_ctx, cancel = context.WithTimeout(ctx, p.attempt_timeout)
			defer cancel()
		}
		addresses, lookup_err := p.lookup(lookup_ctx, p.dns_name)
		return lookup_err == nil && len(addresses) > 0
	}
	dns_ok := false
	if !p.udp_dns {
		dns_ok = system_lookup()
	}

	rtts, failed := p.dial_all(ctx, target)
	<-udp_done
	if len(rtts) == 0 && ctx.Err() == nil && target == p.dial_target && p.fallback_target != "" && p.fallback_allowed != nil && p.fallback_allowed() {
		target = p.fallback_target
		rtts, failed = p.dial_all(ctx, target)
		// The figures describe the server the sample names.
		if len(rtts) > 0 {
			udp = p.udp_probe(ctx, target)
		}
	}
	if p.udp_dns {
		if udp.sent > 0 {
			dns_ok = udp.dns_ok
		} else if ctx.Err() == nil {
			dns_ok = system_lookup()
		}
	}
	prompt, resent := split_resent(rtts)
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
	if err := ctx.Err(); err != nil {
		return diagnostics.Sample{}, err
	}

	// A dial that connected only after its SYN was sent again took the
	// retransmission timer plus a round trip: averaging it in reported a
	// lost packet as a second of latency. It is counted as what it is.
	// With every attempt resent there is no round trip to report, and the
	// sample carries none.
	var rtt time.Duration
	if len(prompt) > 0 {
		rtt = mean_rtt(prompt)
	}
	return diagnostics.Sample{
		Rtt:                rtt,
		Resent:             float64(resent) / float64(p.attempts),
		Udp_measured:       udp.sent > 0,
		Udp_rtt:            udp.rtt,
		Udp_loss:           udp.loss(),
		Measurement_target: target,

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
func (p *Network_prober) dial_all(ctx context.Context, target string) ([]time.Duration, int) {
	type outcome struct {
		rtt    time.Duration
		ok     bool
		reason string
	}
	outcomes := make([]outcome, p.attempts)

	var wg sync.WaitGroup
	wg.Add(p.attempts)
	for i := 0; i < p.attempts; i++ {
		go func(i int) {
			defer wg.Done()
			rtt, err := p.dial_one(ctx, target)
			reason := ""
			if err != nil {
				reason = probe_failure_reason(err)
			}
			outcomes[i] = outcome{rtt: rtt, ok: err == nil, reason: reason}
		}(i)
	}
	wg.Wait()

	rtts := make([]time.Duration, 0, p.attempts)
	failed := 0
	timeouts, failures := 0, 0
	for _, o := range outcomes {
		if o.ok {
			rtts = append(rtts, o.rtt)
		} else {
			failed++
			if o.reason == "timeout" {
				timeouts++
			} else {
				failures++
			}
		}
	}
	if ctx.Err() == nil {
		p.log_target(target, rtts, failed, timeouts, failures)
	}
	return rtts, failed
}

// dial_one performs one round trip: dial target within its own
// Attempt_timeout, timed from just before the dial to just after it
// returns, and close the connection the moment it is no longer needed.
func (p *Network_prober) dial_one(ctx context.Context, target string) (time.Duration, error) {
	attempt_ctx := ctx
	if p.attempt_timeout > 0 {
		var cancel context.CancelFunc
		attempt_ctx, cancel = context.WithTimeout(ctx, p.attempt_timeout)
		defer cancel()
	}

	start := p.now()
	conn, err := p.dial(attempt_ctx, "tcp", target)
	elapsed := p.now().Sub(start)
	if err != nil {
		return 0, err
	}

	_ = conn.Close()
	return elapsed, nil
}

// udp_outcome is one UDP burst: how many queries went out, how many came
// back in time, their mean round trip, and whether any answer carried an
// address for Dns_name.
type udp_outcome struct {
	sent, answered int
	rtt            time.Duration
	dns_ok         bool
}

func (u udp_outcome) loss() float64 {
	if u.sent == 0 {
		return 0
	}
	return float64(u.sent-u.answered) / float64(u.sent)
}

// udp_probe sends udp_queries DNS queries for dns_name to port 53 of
// target's host, udp_query_spacing apart on one socket, and waits up to
// udp_timeout after the last for the answers. Only an address literal is
// probed: resolving a name here would ask the very resolver being
// measured, and the system's on top of it.
func (p *Network_prober) udp_probe(ctx context.Context, target string) udp_outcome {
	var result udp_outcome
	if p.udp_queries == 0 {
		return result
	}
	address, err := netip.ParseAddrPort(target)
	if err != nil {
		return result
	}
	server := netip.AddrPortFrom(address.Addr(), 53).String()
	conn, err := p.dial_udp(ctx, "udp", server)
	if err != nil {
		return result
	}
	defer conn.Close()

	base := uint16(p.now().UnixNano())
	var sent_mu sync.Mutex
	sent_at := make([]time.Time, p.udp_queries)
	answered := make([]bool, p.udp_queries)
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := range p.udp_queries {
			if ctx.Err() != nil {
				return
			}
			if i > 0 {
				time.Sleep(udp_query_spacing)
			}
			query := dns_query(base+uint16(i), p.dns_name)
			sent_mu.Lock()
			sent_at[i] = p.now()
			sent_mu.Unlock()
			if _, err := conn.Write(query); err != nil {
				// Nothing left the machine: not a lost datagram.
				sent_mu.Lock()
				sent_at[i] = time.Time{}
				sent_mu.Unlock()
				return
			}
		}
	}()
	deadline := time.Now().Add(time.Duration(p.udp_queries-1)*udp_query_spacing + udp_timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	// A cancelled probe stops waiting for answers now, not at the deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()
	var total time.Duration
	buffer := make([]byte, 1500)
	for result.answered < p.udp_queries {
		n, err := conn.Read(buffer)
		if err != nil {
			break
		}
		at := p.now()
		id, rcode, answers, ok := dns_reply(buffer[:n])
		index := int(id - base)
		if !ok || index >= p.udp_queries || answered[index] {
			continue
		}
		sent_mu.Lock()
		sent := sent_at[index]
		sent_mu.Unlock()
		if sent.IsZero() {
			continue
		}
		answered[index] = true
		result.answered++
		total += at.Sub(sent)
		if rcode == 0 && answers > 0 {
			result.dns_ok = true
		}
	}
	writer.Wait()
	for _, sent := range sent_at {
		if !sent.IsZero() {
			result.sent++
		}
	}
	if result.answered > 0 {
		result.rtt = total / time.Duration(result.answered)
	}
	return result
}

// dns_query is a recursive query for the A record of name.
func dns_query(id uint16, name string) []byte {
	query := []byte{byte(id >> 8), byte(id), 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	return append(query, 0, 0, 1, 0, 1)
}

// dns_reply reads the header of an answer: its id, response code and
// answer count. ok is false for anything that is not a response.
func dns_reply(packet []byte) (id uint16, rcode, answers int, ok bool) {
	if len(packet) < 12 || packet[2]&0x80 == 0 {
		return 0, 0, 0, false
	}
	return uint16(packet[0])<<8 | uint16(packet[1]), int(packet[3] & 0x0f), int(packet[6])<<8 | int(packet[7]), true
}

// Syn_resend_after is when TCP sends an unanswered SYN again: Windows'
// initial retransmission timeout, 1000 ms (netsh int tcp show global). A
// dial that took at least this long was connected by a resent SYN.
const Syn_resend_after = time.Second

// split_resent separates the dials that connected promptly from the count
// of those that needed their SYN resent.
func split_resent(rtts []time.Duration) (prompt []time.Duration, resent int) {
	for _, rtt := range rtts {
		if rtt >= Syn_resend_after {
			resent++
		} else {
			prompt = append(prompt, rtt)
		}
	}
	return prompt, resent
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

// Keep dial errors categorical: arbitrary resolver or transport payloads may contain private data.
func probe_failure_reason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var network_error net.Error
	if errors.As(err, &network_error) && network_error.Timeout() {
		return "timeout"
	}
	return "connection_failed"
}

// One summary per target and status, with immediate transitions and minute heartbeats.
type probe_log_state struct {
	signature string
	at        time.Time
}

func (p *Network_prober) log_target(target string, rtts []time.Duration, failed, timeouts, failures int) {
	p.log_mu.Lock()
	defer p.log_mu.Unlock()
	if p.target_logs == nil {
		p.target_logs = make(map[string]probe_log_state)
	}
	// Slow successful dials are evidence, not a retransmission diagnosis.
	// Keep their full duration and range; never subtract a presumed SYN retry.
	slow := 0
	var shortest, longest time.Duration
	for i, rtt := range rtts {
		if i == 0 || rtt < shortest {
			shortest = rtt
		}
		if rtt > longest {
			longest = rtt
		}
		if rtt >= Syn_resend_after {
			slow++
		}
	}
	signature := fmt.Sprintf("%d/%d/%d/%d", failed, timeouts, failures, slow)
	now := p.now()
	previous, exists := p.target_logs[target]
	if exists && previous.signature == signature && now.Sub(previous.at) < time.Minute {
		return
	}
	p.target_logs[target] = probe_log_state{signature, now}
	if failed == 0 && slow == 0 && target != p.fallback_target && (!exists || previous.signature == signature) {
		return
	}
	target_kind := "primary"
	if target == p.fallback_target {
		target_kind = "fallback"
	}
	attrs := []any{"target", target_kind, "failed_attempts", failed, "attempts", p.attempts, "timeouts", timeouts, "connection_failures", failures, "fallback", target == p.fallback_target}
	if len(rtts) > 0 {
		attrs = append(attrs, "rtt", mean_rtt(rtts), "rtt_min", shortest, "rtt_max", longest, "slow_successes", slow)
	}
	if failed > 0 {
		p.logger.Warn("probe target measurement failed", attrs...)
	} else if slow > 0 {
		p.logger.Info("probe target slow TCP connect", attrs...)
	} else if target == p.fallback_target {
		p.logger.Info("probe measured through fallback target", attrs...)
	} else {
		p.logger.Info("probe target recovered", attrs...)
	}
}
