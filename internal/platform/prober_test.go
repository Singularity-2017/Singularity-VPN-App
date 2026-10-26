package platform

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"singularity-vpn-app/internal/diagnostics"
)

// --- fakes ---------------------------------------------------------------

// stepping_clock is a fake clock that moves only when something tells it
// to, never merely because it was read. That property is what makes an
// exact assertion on a measured round trip possible at all: Probe's
// attempts run concurrently and share this one clock, so a clock that
// advanced on every read would give each attempt a bracket that depended
// on how the goroutines happened to interleave.
type stepping_clock struct {
	mu sync.Mutex
	at time.Time
}

func new_stepping_clock() *stepping_clock {
	return &stepping_clock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *stepping_clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *stepping_clock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// fixed_clock is a Now for the tests that never look at a duration.
func fixed_clock() func() time.Time {
	return new_stepping_clock().now
}

// fake_conn is a net.Conn that records whether it was closed. Every method
// besides Close is unused by Network_prober and exists only to satisfy
// net.Conn.
type fake_conn struct {
	mu     sync.Mutex
	closed int
}

func (c *fake_conn) Read(b []byte) (int, error)  { return 0, nil }
func (c *fake_conn) Write(b []byte) (int, error) { return len(b), nil }
func (c *fake_conn) Close() error {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
	return nil
}
func (c *fake_conn) LocalAddr() net.Addr                { return nil }
func (c *fake_conn) RemoteAddr() net.Addr               { return nil }
func (c *fake_conn) SetDeadline(t time.Time) error      { return nil }
func (c *fake_conn) SetReadDeadline(t time.Time) error  { return nil }
func (c *fake_conn) SetWriteDeadline(t time.Time) error { return nil }

func (c *fake_conn) close_count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// dial_outcome is one scripted answer a fake dialler gives: either a
// success that moves the clock forward by after, standing in for the time
// a real dial would have taken, or an error and no time at all.
type dial_outcome struct {
	after time.Duration
	err   error
}

// scripted_dialer hands out dial_outcome values in order, one per call,
// advances the clock by a successful outcome's duration while it is
// standing in for the dial, and records every connection it opened so a
// test can assert every one of them was closed. It never touches the
// network.
//
// Which concurrent attempt draws which outcome is not fixed, and it does
// not need to be: the mean and the loss fraction do not depend on the
// order. An assertion on an exact round trip does, which is why the only
// test making one scripts exactly one success -- see
// Test_rtt_is_measured_around_the_dial.
type scripted_dialer struct {
	mu       sync.Mutex
	clock    *stepping_clock
	outcomes []dial_outcome
	calls    int
	conns    []*fake_conn
}

func (d *scripted_dialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	index := d.calls
	d.calls++
	var outcome dial_outcome
	if index < len(d.outcomes) {
		outcome = d.outcomes[index]
	}
	d.mu.Unlock()

	if outcome.err != nil {
		return nil, outcome.err
	}
	if d.clock != nil {
		d.clock.advance(outcome.after)
	}
	conn := &fake_conn{}
	d.mu.Lock()
	d.conns = append(d.conns, conn)
	d.mu.Unlock()
	return conn, nil
}

func (d *scripted_dialer) call_count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *scripted_dialer) total_closes() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for _, c := range d.conns {
		total += c.close_count()
	}
	return total
}

func (d *scripted_dialer) open_count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.conns)
}

// never_dial fails a test outright if it is ever called -- used to assert a
// down tunnel is refused before any network attempt.
func never_dial(t *testing.T) func(ctx context.Context, network, address string) (net.Conn, error) {
	t.Helper()
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		t.Fatalf("dial was called through a tunnel that was reported down")
		return nil, nil
	}
}

func always_up() bool   { return true }
func always_down() bool { return false }

func lookup_ok(addresses ...string) func(ctx context.Context, host string) ([]string, error) {
	return func(ctx context.Context, host string) ([]string, error) {
		return addresses, nil
	}
}

func lookup_err(err error) func(ctx context.Context, host string) ([]string, error) {
	return func(ctx context.Context, host string) ([]string, error) {
		return nil, err
	}
}

func base_config() Prober_config {
	return Prober_config{
		Tunnel_up:   always_up,
		Dns_name:    "example.test",
		Dial_target: "example.test:443",
	}
}

// --- tests -----------------------------------------------------------------

func Test_a_probe_through_a_down_tunnel_reports_the_tunnel_down(t *testing.T) {
	config := base_config()
	config.Tunnel_up = always_down
	config.Dial = never_dial(t)
	config.Now = fixed_clock()

	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatalf("New_network_prober: %v", err)
	}

	_, err = prober.Probe(context.Background())
	if !errors.Is(err, diagnostics.Err_tunnel_down) {
		t.Fatalf("Probe() error = %v, want Err_tunnel_down", err)
	}
}

func Test_a_probe_that_reaches_nothing_reports_no_internet(t *testing.T) {
	dialer := &scripted_dialer{outcomes: []dial_outcome{
		{err: errors.New("refused")},
		{err: errors.New("refused")},
		{err: errors.New("refused")},
		{err: errors.New("refused")},
	}}

	config := base_config()
	config.Lookup = lookup_ok("203.0.113.1")
	config.Dial = dialer.dial
	config.Now = fixed_clock()

	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatalf("New_network_prober: %v", err)
	}

	sample, err := prober.Probe(context.Background())
	if !errors.Is(err, diagnostics.Err_no_internet) {
		t.Fatalf("Probe() error = %v, want Err_no_internet", err)
	}
	if sample != (diagnostics.Sample{}) {
		t.Fatalf("Probe() sample = %+v, want the zero value", sample)
	}
	// Every attempt is made before the verdict. A probe that gave up after
	// the first refusal would call one failed connection a lost tunnel.
	if got := dialer.call_count(); got != Default_attempts {
		t.Fatalf("dialled %d times, want all %d attempts before deciding nothing is reachable", got, Default_attempts)
	}
}

// Test_rtt_is_measured_around_the_dial is the only test that asserts an
// exact duration out of Probe, and it scripts exactly one success for that
// reason: the failing attempts move the clock by nothing at all, so the one
// successful attempt's bracket contains its own dial and nothing else, no
// matter how the four goroutines interleave. It is also what proves a
// failed attempt contributes no measurement -- three zero-length failures
// counted into the mean would make this 25 milliseconds rather than 100.
func Test_rtt_is_measured_around_the_dial(t *testing.T) {
	clock := new_stepping_clock()
	dialer := &scripted_dialer{clock: clock, outcomes: []dial_outcome{
		{after: 100 * time.Millisecond},
		{err: errors.New("timed out")},
		{err: errors.New("timed out")},
		{err: errors.New("timed out")},
	}}

	config := base_config()
	config.Lookup = lookup_ok("203.0.113.1")
	config.Dial = dialer.dial
	config.Now = clock.now
	config.Attempts = 4

	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatalf("New_network_prober: %v", err)
	}

	sample, err := prober.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if sample.Rtt != 100*time.Millisecond {
		t.Fatalf("Rtt = %v, want %v -- the one attempt that succeeded, timed around its own dial, with the three that failed contributing nothing",
			sample.Rtt, 100*time.Millisecond)
	}
}

// Test_the_mean_averages_only_what_was_measured tests the arithmetic on its
// own. It cannot be tested exactly through Probe with more than one
// successful attempt: the attempts run concurrently and share one clock, so
// which of them a given advance lands inside is not something a test can
// fix. Splitting it out is what lets both halves be exact -- the bracket
// above, the average here.
func Test_the_mean_averages_only_what_was_measured(t *testing.T) {
	cases := []struct {
		name string
		rtts []time.Duration
		want time.Duration
	}{
		{"one", []time.Duration{40 * time.Millisecond}, 40 * time.Millisecond},
		{"three", []time.Duration{10 * time.Millisecond, 30 * time.Millisecond, 20 * time.Millisecond}, 20 * time.Millisecond},
		{"rounds down", []time.Duration{1, 2}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mean_rtt(tc.rtts); got != tc.want {
				t.Fatalf("mean_rtt(%v) = %v, want %v", tc.rtts, got, tc.want)
			}
		})
	}
}

func Test_packet_loss_is_the_fraction_that_failed(t *testing.T) {
	cases := []struct {
		name    string
		failed  int
		succeed int
		want    float64
	}{
		{"none failed", 0, 4, 0},
		{"one of four failed", 1, 3, 0.25},
		{"three of four failed", 3, 1, 0.75},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var outcomes []dial_outcome
			for i := 0; i < tc.failed; i++ {
				outcomes = append(outcomes, dial_outcome{err: errors.New("failed")})
			}
			for i := 0; i < tc.succeed; i++ {
				outcomes = append(outcomes, dial_outcome{after: 5 * time.Millisecond})
			}
			dialer := &scripted_dialer{outcomes: outcomes}

			config := base_config()
			config.Lookup = lookup_ok("203.0.113.1")
			config.Dial = dialer.dial
			config.Now = fixed_clock()
			config.Attempts = 4

			prober, err := New_network_prober(config)
			if err != nil {
				t.Fatalf("New_network_prober: %v", err)
			}

			sample, err := prober.Probe(context.Background())
			if tc.succeed == 0 {
				if !errors.Is(err, diagnostics.Err_no_internet) {
					t.Fatalf("Probe() error = %v, want Err_no_internet", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Probe() error = %v", err)
			}
			if sample.Packet_loss != tc.want {
				t.Fatalf("Packet_loss = %v, want %v", sample.Packet_loss, tc.want)
			}
		})
	}
}

func Test_a_failed_lookup_still_produces_a_sample(t *testing.T) {
	// One success and three failures, so the round trip below is exact for
	// the reason given on Test_rtt_is_measured_around_the_dial.
	clock := new_stepping_clock()
	dialer := &scripted_dialer{clock: clock, outcomes: []dial_outcome{
		{after: 10 * time.Millisecond},
		{err: errors.New("refused")},
		{err: errors.New("refused")},
		{err: errors.New("refused")},
	}}

	config := base_config()
	config.Lookup = lookup_err(errors.New("no such host"))
	config.Dial = dialer.dial
	config.Now = clock.now

	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatalf("New_network_prober: %v", err)
	}

	sample, err := prober.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil -- a broken resolver is not a broken tunnel", err)
	}
	if sample.Dns_ok {
		t.Fatalf("Dns_ok = true, want false")
	}
	if sample.Rtt != 10*time.Millisecond {
		t.Fatalf("Rtt = %v, want %v", sample.Rtt, 10*time.Millisecond)
	}
}

func Test_a_lookup_that_answers_nothing_is_not_ok(t *testing.T) {
	dialer := &scripted_dialer{outcomes: []dial_outcome{
		{after: 10 * time.Millisecond},
		{after: 10 * time.Millisecond},
		{after: 10 * time.Millisecond},
		{after: 10 * time.Millisecond},
	}}

	config := base_config()
	config.Lookup = lookup_ok() // no error, no addresses
	config.Dial = dialer.dial
	config.Now = fixed_clock()

	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatalf("New_network_prober: %v", err)
	}

	sample, err := prober.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if sample.Dns_ok {
		t.Fatalf("Dns_ok = true, want false when the lookup answered nothing")
	}
}

func Test_every_connection_that_opened_is_closed(t *testing.T) {
	dialer := &scripted_dialer{outcomes: []dial_outcome{
		{after: 5 * time.Millisecond},
		{err: errors.New("refused")},
		{after: 5 * time.Millisecond},
		{after: 5 * time.Millisecond},
	}}

	config := base_config()
	config.Lookup = lookup_ok("203.0.113.1")
	config.Dial = dialer.dial
	config.Now = fixed_clock()

	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatalf("New_network_prober: %v", err)
	}

	if _, err := prober.Probe(context.Background()); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}

	if opened := dialer.open_count(); opened != 3 {
		t.Fatalf("opened %d connections, want 3", opened)
	}
	if closed := dialer.total_closes(); closed != dialer.open_count() {
		t.Fatalf("closed %d of %d connections that opened", closed, dialer.open_count())
	}
}

func Test_a_cancelled_context_does_not_produce_a_sample(t *testing.T) {
	dialer := &scripted_dialer{outcomes: []dial_outcome{
		{err: context.Canceled},
		{err: context.Canceled},
		{err: context.Canceled},
		{err: context.Canceled},
	}}

	config := base_config()
	config.Lookup = lookup_err(context.Canceled)
	config.Dial = dialer.dial
	config.Now = fixed_clock()

	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatalf("New_network_prober: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sample, err := prober.Probe(ctx)
	if !errors.Is(err, context.Canceled) {
		// Reporting a cancelled probe as Err_no_internet would be a lie
		// with consequences: Tick counts it as a probe error, and two in a
		// row move a person off their node because the service was asked
		// to shut down.
		t.Fatalf("Probe() error = %v, want context.Canceled rather than a verdict about the tunnel", err)
	}
	if sample != (diagnostics.Sample{}) {
		t.Fatalf("Probe() sample = %+v, want the zero value", sample)
	}
}

func Test_a_prober_without_a_target_is_refused(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Prober_config)
	}{
		{"no Tunnel_up", func(c *Prober_config) { c.Tunnel_up = nil }},
		{"no Dns_name", func(c *Prober_config) { c.Dns_name = "" }},
		{"no Dial_target", func(c *Prober_config) { c.Dial_target = "" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := base_config()
			tc.mutate(&config)

			if _, err := New_network_prober(config); err == nil {
				t.Fatalf("New_network_prober() error = nil, want a refusal")
			}
		})
	}
}

// addresses_dialled records which address every attempt of a probe dialled.
func addresses_dialled(t *testing.T, config Prober_config) []string {
	t.Helper()
	var mu sync.Mutex
	var addresses []string
	config.Lookup = lookup_ok("203.0.113.1")
	config.Now = fixed_clock()
	config.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		addresses = append(addresses, address)
		mu.Unlock()
		return &fake_conn{}, nil
	}
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatalf("New_network_prober: %v", err)
	}
	if _, err := prober.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return addresses
}

func Test_a_probe_dials_the_target_the_tunnel_names_when_it_names_one(t *testing.T) {
	config := base_config()
	config.Dial_target_now = func() string { return "10.29.8.1:53" }
	got := addresses_dialled(t, config)
	if len(got) != Default_attempts {
		t.Fatalf("dialled %d times, want %d", len(got), Default_attempts)
	}
	for _, address := range got {
		if address != "10.29.8.1:53" {
			t.Fatalf("dialled %q, want the target the tunnel named", address)
		}
	}
}

func Test_a_probe_dials_the_static_target_when_the_tunnel_names_none(t *testing.T) {
	config := base_config()
	config.Dial_target_now = func() string { return "" }
	for _, address := range addresses_dialled(t, config) {
		if address != "example.test:443" {
			t.Fatalf("dialled %q, want the static target", address)
		}
	}
}

func Test_fallback_corroborates_only_eligible_full_tunnel(t *testing.T) {
	for _, test := range []struct {
		name          string
		target        string
		allowed       bool
		fallback_ok   bool
		want_sample   bool
		want_fallback bool
	}{
		{"full reachable fallback", "", true, true, true, true},
		{"both targets fail", "", true, false, false, true},
		{"split target", "10.29.8.1:53", true, true, false, false},
		{"ineligible same target", "example.test:443", false, true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := base_config()
			config.Attempts = 1
			config.Lookup = lookup_ok("192.0.2.1")
			config.Fallback_target = "fallback.test:443"
			config.Fallback_allowed = func() bool { return test.allowed }
			config.Dial_target_now = func() string { return test.target }
			var output bytes.Buffer
			config.Logger = slog.New(slog.NewTextHandler(&output, nil))
			fallback_called := false
			config.Dial = func(ctx context.Context, network, target string) (net.Conn, error) {
				if target == config.Fallback_target {
					fallback_called = true
					if test.fallback_ok {
						return &fake_conn{}, nil
					}
				}
				return nil, context.DeadlineExceeded
			}
			prober, err := New_network_prober(config)
			if err != nil {
				t.Fatal(err)
			}
			sample, err := prober.Probe(context.Background())
			if (err == nil) != test.want_sample {
				t.Fatalf("sample error = %v", err)
			}
			if err != nil && !errors.Is(err, diagnostics.Err_no_internet) {
				t.Fatal(err)
			}
			if fallback_called != test.want_fallback {
				t.Fatalf("fallback called = %v", fallback_called)
			}
			if test.want_sample && (!sample.Internet_ok || sample.Packet_loss != 0) {
				t.Fatalf("sample = %+v", sample)
			}
			if !strings.Contains(output.String(), "timeouts=1") {
				t.Fatalf("missing failure reason: %s", &output)
			}
			if test.want_sample && !strings.Contains(output.String(), "probe measured through fallback target") {
				t.Fatal("missing fallback log")
			}
		})
	}
}

func Test_cancelled_probe_does_not_try_fallback(t *testing.T) {
	config := base_config()
	config.Lookup = lookup_ok("192.0.2.1")
	config.Fallback_target = "fallback.test:443"
	config.Fallback_allowed = func() bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	config.Dial = func(ctx context.Context, network, target string) (net.Conn, error) {
		if target == config.Fallback_target {
			t.Error("fallback attempted after cancellation")
		}
		cancel()
		return nil, context.Canceled
	}
	prober, _ := New_network_prober(config)
	_, err := prober.Probe(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func Test_probe_logs_aggregate_rate_limit_and_recover(t *testing.T) {
	var output bytes.Buffer
	clock := new_stepping_clock()
	p, err := New_network_prober(Prober_config{Tunnel_up: always_up, Dns_name: "example.test", Dial_target: "primary", Attempts: 4, Now: clock.now, Logger: slog.New(slog.NewTextHandler(&output, nil))})
	if err != nil {
		t.Fatal(err)
	}
	p.log_target("primary", nil, 4, 3, 1)
	p.log_target("primary", nil, 4, 3, 1)
	if strings.Count(output.String(), "probe target measurement failed") != 1 {
		t.Fatalf("repeated failure not suppressed: %s", &output)
	}
	if !strings.Contains(output.String(), "timeouts=3 connection_failures=1") {
		t.Fatalf("missing aggregate: %s", &output)
	}
	clock.advance(time.Minute)
	p.log_target("primary", nil, 4, 3, 1)
	if strings.Count(output.String(), "probe target measurement failed") != 2 {
		t.Fatal("missing heartbeat")
	}
	p.log_target("primary", []time.Duration{time.Millisecond}, 0, 0, 0)
	if !strings.Contains(output.String(), "probe target recovered") {
		t.Fatal("missing immediate recovery")
	}
	clock.advance(time.Minute)
	p.log_target("primary", []time.Duration{time.Millisecond}, 0, 0, 0)
	if strings.Count(output.String(), "probe target recovered") != 1 {
		t.Fatal("healthy heartbeat falsely reported another recovery")
	}

}

func Test_tcp_duration_is_not_corrected_by_a_presumed_retry_delay(t *testing.T) {
	for _, duration := range []time.Duration{49 * time.Millisecond, 1053 * time.Millisecond} {
		t.Run(duration.String(), func(t *testing.T) {
			clock := new_stepping_clock()
			config := base_config()
			config.Attempts = 1
			config.Now = clock.now
			config.Lookup = func(context.Context, string) ([]string, error) {
				clock.advance(time.Second)
				return []string{"203.0.113.1"}, nil
			}
			config.Dial = func(context.Context, string, string) (net.Conn, error) {
				clock.advance(duration)
				return &slow_close_conn{clock: clock}, nil
			}
			prober, err := New_network_prober(config)
			if err != nil {
				t.Fatal(err)
			}
			sample, err := prober.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			// A prompt dial is its own round trip. A dial that needed its
			// SYN resent is loss: never a "corrected" round trip with the
			// timer subtracted, never a second of latency.
			want_rtt, want_resent := duration, 0.0
			if duration >= Syn_resend_after {
				want_rtt, want_resent = 0, 1
			}
			if sample.Rtt != want_rtt || sample.Resent != want_resent {
				t.Fatalf("RTT = %v, resent = %v; want %v, %v", sample.Rtt, sample.Resent, want_rtt, want_resent)
			}
		})
	}
}

func Test_resent_dials_are_kept_out_of_the_round_trip(t *testing.T) {
	// One of four concurrent dials resent: the journal used to read
	// "309 ms instead of 56 ms" for this.
	prompt, resent := split_resent([]time.Duration{56 * time.Millisecond, 1056 * time.Millisecond, 55 * time.Millisecond, 57 * time.Millisecond})
	if resent != 1 || len(prompt) != 3 || mean_rtt(prompt) != 56*time.Millisecond {
		t.Fatalf("prompt = %v, resent = %d", prompt, resent)
	}
}

type slow_close_conn struct {
	fake_conn
	clock *stepping_clock
}

func (c *slow_close_conn) Close() error {
	c.clock.advance(time.Second)
	return c.fake_conn.Close()
}

func Test_probe_attempts_start_in_parallel(t *testing.T) {
	config := base_config()
	config.Lookup = lookup_ok("203.0.113.1")
	started := make(chan struct{}, Default_attempts)
	release := make(chan struct{})
	config.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		started <- struct{}{}
		select {
		case <-release:
			return &fake_conn{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := prober.Probe(ctx); done <- err }()
	for i := 0; i < Default_attempts; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("attempts were serialized")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func Test_slow_successful_dials_log_the_unmodified_range(t *testing.T) {
	var output bytes.Buffer
	config := base_config()
	config.Logger = slog.New(slog.NewTextHandler(&output, nil))
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		prober.log_target("example.test:443", []time.Duration{49 * time.Millisecond, 1053 * time.Millisecond}, 0, 0, 0)
	}
	line := output.String()
	if strings.Count(line, "probe target slow TCP connect") != 1 || !strings.Contains(line, "rtt_min=49ms rtt_max=1.053s slow_successes=1") {
		t.Fatalf("missing range or rate limiting: %s", line)
	}
}

func Test_cancelled_successful_dial_does_not_publish_a_sample(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := base_config()
	config.Attempts = 1
	config.Lookup = lookup_ok("203.0.113.1")
	config.Dial = func(context.Context, string, string) (net.Conn, error) {
		cancel()
		return &fake_conn{}, nil
	}
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prober.Probe(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe() = %v, want cancellation", err)
	}
}

// echo_resolver is a UDP conn to a resolver that answers every query with
// one address, except the ones whose position is in drop.
type echo_resolver struct {
	mu       sync.Mutex
	address  string
	written  int
	drop     map[int]bool
	answers  chan []byte
	deadline time.Time
}

func new_echo_resolver(drop ...int) *echo_resolver {
	r := &echo_resolver{drop: map[int]bool{}, answers: make(chan []byte, 64)}
	for _, i := range drop {
		r.drop[i] = true
	}
	return r
}

func (r *echo_resolver) dial(_ context.Context, network, address string) (net.Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.address = network + " " + address
	return r, nil
}

func (r *echo_resolver) Write(b []byte) (int, error) {
	r.mu.Lock()
	i := r.written
	r.written++
	r.mu.Unlock()
	if !r.drop[i] {
		answer := append([]byte{}, b...)
		answer[2] |= 0x80 // a response
		answer[7] = 1     // one answer
		go func() {
			time.Sleep(5 * time.Millisecond) // a server is never instant
			r.answers <- answer
		}()
	}
	return len(b), nil
}

// Read, like a real socket's, gives up as soon as the deadline passes, a
// deadline moved while it waits included.
func (r *echo_resolver) Read(b []byte) (int, error) {
	for {
		r.mu.Lock()
		expired := !time.Now().Before(r.deadline)
		r.mu.Unlock()
		if expired {
			return 0, os.ErrDeadlineExceeded
		}
		select {
		case answer := <-r.answers:
			return copy(b, answer), nil
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (r *echo_resolver) SetReadDeadline(t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadline = t
	return nil
}

func (r *echo_resolver) Close() error                       { return nil }
func (r *echo_resolver) LocalAddr() net.Addr                { return nil }
func (r *echo_resolver) RemoteAddr() net.Addr               { return nil }
func (r *echo_resolver) SetDeadline(t time.Time) error      { return r.SetReadDeadline(t) }
func (r *echo_resolver) SetWriteDeadline(t time.Time) error { return nil }

func Test_the_probe_measures_udp_loss_the_way_games_see_it(t *testing.T) {
	resolver := new_echo_resolver(2, 5)
	config := base_config()
	config.Dial_target = "192.0.2.53:53"
	config.Dial = func(context.Context, string, string) (net.Conn, error) { return &fake_conn{}, nil }
	config.Dial_udp = resolver.dial
	config.Udp_queries = Default_udp_queries
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	sample, err := prober.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolver.address != "udp 192.0.2.53:53" {
		t.Fatalf("UDP went to %q, want the probed server's port 53", resolver.address)
	}
	if !sample.Udp_measured || sample.Udp_loss != 2.0/8 || sample.Udp_rtt <= 0 {
		t.Fatalf("udp measured %v, loss %v, rtt %v; want two of eight lost", sample.Udp_measured, sample.Udp_loss, sample.Udp_rtt)
	}
	// With no Lookup the answers stand for the DNS check: the system
	// resolver, and the DNS Client service behind it, are never asked.
	if !sample.Dns_ok {
		t.Fatal("answered queries did not count as working DNS")
	}
}

func Test_a_named_target_gets_no_udp_burst(t *testing.T) {
	config := base_config() // example.test:443: a name, not an address
	config.Dial = func(context.Context, string, string) (net.Conn, error) { return &fake_conn{}, nil }
	config.Dial_udp = never_dial(t)
	config.Udp_queries = Default_udp_queries
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	sample, err := prober.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sample.Udp_measured || sample.Dns_ok {
		t.Fatalf("sample = %+v, want nothing measured over UDP", sample)
	}
}

// silent_resolver takes every query and never answers.
type silent_resolver struct{ echo_resolver }

func (r *silent_resolver) Write(b []byte) (int, error) { return len(b), nil }

func Test_a_cancelled_probe_stops_waiting_for_udp_answers(t *testing.T) {
	resolver := &silent_resolver{echo_resolver{answers: make(chan []byte)}}
	config := base_config()
	config.Dial_target = "192.0.2.53:53"
	config.Dial = func(context.Context, string, string) (net.Conn, error) { return &fake_conn{}, nil }
	config.Dial_udp = func(context.Context, string, string) (net.Conn, error) { return resolver, nil }
	config.Udp_queries = Default_udp_queries
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, _ = prober.Probe(ctx)
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("Probe returned %v after its cancel; the UDP wait must end with the context", waited)
	}
}

// failing_writer is a UDP socket whose every send fails.
type failing_writer struct{ echo_resolver }

func (r *failing_writer) Write(b []byte) (int, error) { return 0, errors.New("no buffer space") }

func Test_queries_that_never_left_are_not_lost_datagrams(t *testing.T) {
	resolver := &failing_writer{echo_resolver{answers: make(chan []byte)}}
	config := base_config()
	config.Dial_target = "192.0.2.53:53"
	config.Dial = func(context.Context, string, string) (net.Conn, error) { return &fake_conn{}, nil }
	config.Dial_udp = func(context.Context, string, string) (net.Conn, error) { return resolver, nil }
	config.Lookup = lookup_ok("192.0.2.1")
	config.Udp_queries = Default_udp_queries
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	sample, err := prober.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sample.Udp_measured || sample.Udp_loss != 0 {
		t.Fatalf("udp measured %v, loss %v; nothing was sent, so nothing was measured", sample.Udp_measured, sample.Udp_loss)
	}
}
