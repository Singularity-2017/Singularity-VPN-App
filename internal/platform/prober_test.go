package platform

import (
	"context"
	"errors"
	"net"
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
