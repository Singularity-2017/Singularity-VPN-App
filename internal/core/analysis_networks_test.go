package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"singularity-vpn-app/internal/ipc"
)

// fake_network is the source of the network key: whatever the test says the
// network under the machine is, or why it cannot be told.
type fake_network struct {
	mu  sync.Mutex
	key string
	err error
}

func (n *fake_network) Key(ctx context.Context) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.key, n.err
}

func (n *fake_network) set(key string) {
	n.mu.Lock()
	n.key, n.err = key, nil
	n.mu.Unlock()
}

func (n *fake_network) fail(err error) {
	n.mu.Lock()
	n.err = err
	n.mu.Unlock()
}

// with_network gives the application a network to be on.
func with_network(f *fixture, key string) *fake_network {
	n := &fake_network{key: key}
	f.app.network_key = n.Key
	return n
}

// test_clock is a clock the test moves while the application's goroutines
// read it.
type test_clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *test_clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *test_clock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

func with_clock(f *fixture, start time.Time) *test_clock {
	c := &test_clock{at: start}
	f.app.now = c.now
	return c
}

// log_buffer is a log written from the application's goroutines and read by
// the test.
type log_buffer struct {
	mu   sync.Mutex
	text bytes.Buffer
}

func (b *log_buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *log_buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// logging captures what the application logs, to check what it does not say.
func logging(f *fixture) *log_buffer {
	out := &log_buffer{}
	f.app.logger = slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return out
}

// set_analysis_timers makes the pacing of the automatic runs short for a
// test, and puts it back after.
func set_analysis_timers(t *testing.T, check, start, down time.Duration) {
	t.Helper()
	was_check, was_start, was_down := analysis_check_every, analysis_start_settle, analysis_down_settle
	analysis_check_every, analysis_start_settle, analysis_down_settle = check, start, down
	t.Cleanup(func() {
		analysis_check_every, analysis_start_settle, analysis_down_settle = was_check, was_start, was_down
	})
}

func Test_the_day_rule_for_an_automatic_run(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		measured map[string]time.Time
		network  string
		want     error
	}{
		{"a network never measured", nil, "a", nil},
		{"measured a moment ago", map[string]time.Time{"a": now.Add(-time.Second)}, "a", err_analysis_not_due},
		{"measured an hour ago", map[string]time.Time{"a": now.Add(-time.Hour)}, "a", err_analysis_not_due},
		{"measured just under a day ago", map[string]time.Time{"a": now.Add(-24*time.Hour + time.Second)}, "a", err_analysis_not_due},
		{"measured a day ago", map[string]time.Time{"a": now.Add(-24 * time.Hour)}, "a", nil},
		{"measured a week ago", map[string]time.Time{"a": now.Add(-7 * 24 * time.Hour)}, "a", nil},
		{"only another network measured", map[string]time.Time{"b": now.Add(-time.Minute)}, "a", nil},
		{"measured in the future", map[string]time.Time{"a": now.Add(time.Hour)}, "a", nil},
		{"the network is not known", map[string]time.Time{"a": now.Add(-time.Hour)}, "", err_analysis_unknown_network},
		{"the network is not known and nothing is remembered", nil, "", err_analysis_unknown_network},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := analysis_due(c.measured, c.network, now); !errors.Is(got, c.want) || (c.want == nil) != (got == nil) {
				t.Fatalf("analysis_due() = %v, want %v", got, c.want)
			}
		})
	}
}

// Test_an_automatic_run_needs_the_whole_rule is the rule as the application
// applies it: signed in, connection down, network known, and not measured
// within the day.
func Test_an_automatic_run_needs_the_whole_rule(t *testing.T) {
	cases := []struct {
		name        string
		signed_in   bool
		connected   bool
		network     string
		network_err error
		measured    func(now time.Time) map[string]time.Time
		want        error // nil: a run starts
	}{
		{name: "a new network", signed_in: true, network: "a"},
		{name: "measured an hour ago", signed_in: true, network: "a",
			measured: func(now time.Time) map[string]time.Time { return map[string]time.Time{"a": now.Add(-time.Hour)} },
			want:     err_analysis_not_due},
		{name: "measured 25 hours ago", signed_in: true, network: "a",
			measured: func(now time.Time) map[string]time.Time { return map[string]time.Time{"a": now.Add(-25 * time.Hour)} }},
		{name: "measured on another network only", signed_in: true, network: "a",
			measured: func(now time.Time) map[string]time.Time { return map[string]time.Time{"b": now.Add(-time.Minute)} }},
		{name: "the network is not known", signed_in: true, network: "", want: err_analysis_unknown_network},
		{name: "the network key cannot be read", signed_in: true, network: "a", network_err: errors.New("no adapter"), want: err_analysis_unknown_network},
		{name: "the connection is up", signed_in: true, connected: true, network: "a", want: ipc.Err_tunnel_up},
		{name: "not signed in", network: "a", want: ipc.Err_not_signed_in},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := new_fixture(t)
			with_catalog(f)
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			with_clock(f, now)
			network := with_network(f, c.network)
			if c.network_err != nil {
				network.fail(c.network_err)
			}
			if c.signed_in {
				sign_in(t, f)
			}
			if c.connected {
				connect(t, f)
			}
			if c.measured != nil {
				f.app.analysis.networks, f.app.analysis.networks_loaded = c.measured(now), true
			}

			_, flight, err := f.app.begin_analysis(context.Background(), true)
			if c.want != nil {
				if !errors.Is(err, c.want) {
					t.Fatalf("begin_analysis(automatic) = %v, want %v", err, c.want)
				}
				if f.reach_prober.calls.Load() != 0 {
					t.Fatal("the network was probed although the run was refused")
				}
				return
			}
			if err != nil || flight == nil {
				t.Fatalf("begin_analysis(automatic) = %v, flight %v; want a run", err, flight)
			}
			<-flight.done
			if flight.err != nil || f.reach_prober.calls.Load() != 1 {
				t.Fatalf("the run ended with %v after %d probes", flight.err, f.reach_prober.calls.Load())
			}
		})
	}
}

func Test_a_manual_run_does_not_wait_for_the_day(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	network := with_network(f, "a")
	clock := with_clock(f, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))

	if _, err := f.app.Analyze(context.Background()); err != nil {
		t.Fatalf("first Analyze() = %v", err)
	}
	clock.advance(2 * time.Minute)
	if _, err := f.app.Analyze(context.Background()); err != nil {
		t.Fatalf("second Analyze() = %v", err)
	}
	if got := f.reach_prober.calls.Load(); got != 2 {
		t.Fatalf("probes = %d, want a manual run each time it is asked (after the reuse window)", got)
	}

	// And on a network that cannot be told.
	network.set("")
	clock.advance(2 * time.Minute)
	if _, err := f.app.Analyze(context.Background()); err != nil {
		t.Fatalf("Analyze() on an unknown network = %v", err)
	}
	if got := f.reach_prober.calls.Load(); got != 3 {
		t.Fatalf("probes = %d, want 3", got)
	}
}

func Test_a_successful_run_marks_its_network_and_a_failed_one_does_not(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	with_network(f, "a")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	with_clock(f, now)

	f.reach_prober.err = errors.New("no network")
	if _, err := f.app.Analyze(context.Background()); err == nil {
		t.Fatal("Analyze() with the probes failing = nil")
	}
	f.analysis_networks(t, func(m map[string]time.Time) {
		if len(m) != 0 {
			t.Fatalf("a failed run marked %v", m)
		}
	})

	f.reach_prober.err = nil
	if _, err := f.app.Analyze(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.analysis_networks(t, func(m map[string]time.Time) {
		if at, ok := m["a"]; !ok || !at.Equal(now) || len(m) != 1 {
			t.Fatalf("after a run the networks are %v, want only a at %v", m, now)
		}
	})
}

func (f *fixture) analysis_networks(t *testing.T, check func(map[string]time.Time)) {
	t.Helper()
	f.app.analysis.mu.Lock()
	defer f.app.analysis.mu.Unlock()
	check(f.app.analysis.networks)
}

func Test_a_run_that_ends_on_another_network_is_discarded(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	network := with_network(f, "a")
	answer := start_blocked_analysis(t, f)

	network.set("b")
	close(f.reach_prober.release)

	if err := await_answer(t, answer); !errors.Is(err, err_analysis_network_changed) {
		t.Fatalf("Analyze() = %v, want it discarded for the network changing", err)
	}
	if last, _ := f.app.Analysis(context.Background()); last.Present {
		t.Fatalf("a discarded run was kept: %+v", last)
	}
	f.analysis_networks(t, func(m map[string]time.Time) {
		if len(m) != 0 {
			t.Fatalf("a discarded run marked %v", m)
		}
	})
}

func Test_a_manual_run_on_an_unknown_network_marks_nothing(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	with_network(f, "")
	if _, err := f.app.Analyze(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.analysis_networks(t, func(m map[string]time.Time) {
		if len(m) != 0 {
			t.Fatalf("an unknown network was marked: %v", m)
		}
	})
}

func Test_prune_networks(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	t.Run("what is not worth keeping goes", func(t *testing.T) {
		m := map[string]time.Time{
			"fresh":     now.Add(-time.Hour),
			"month_old": now.Add(-29 * 24 * time.Hour),
			"too_old":   now.Add(-31 * 24 * time.Hour),
			"future":    now.Add(time.Minute),
			"zero":      {},
			"":          now,
			strings.Repeat("k", network_key_max_length+1): now,
		}
		prune_networks(m, now)
		if len(m) != 2 || m["fresh"].IsZero() || m["month_old"].IsZero() {
			t.Fatalf("after pruning: %v, want fresh and month_old only", m)
		}
	})
	t.Run("the newest sixty-four stay", func(t *testing.T) {
		m := map[string]time.Time{}
		for i := 0; i < analysis_networks_max+6; i++ {
			m[fmt.Sprintf("n%02d", i)] = now.Add(-time.Duration(i) * time.Minute)
		}
		prune_networks(m, now)
		if len(m) != analysis_networks_max {
			t.Fatalf("%d networks stay, want %d", len(m), analysis_networks_max)
		}
		for i := 0; i < analysis_networks_max+6; i++ {
			_, kept := m[fmt.Sprintf("n%02d", i)]
			if kept != (i < analysis_networks_max) {
				t.Fatalf("n%02d kept = %v", i, kept)
			}
		}
	})
}

func Test_the_networks_are_remembered_across_a_restart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analysis-networks.json")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	first := new_fixture(t)
	sign_in(t, first)
	with_catalog(first)
	first.app.analysis_networks_path = path
	with_network(first, "hashed-a")
	with_clock(first, now)
	if _, err := first.app.Analyze(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the networks file was not written: %v", err)
	}
	var stored map[string]time.Time
	if err := json.Unmarshal(raw, &stored); err != nil || len(stored) != 1 || !stored["hashed-a"].Equal(now) {
		t.Fatalf("the networks file holds %s, want only hashed-a at %v", raw, now)
	}

	second := new_fixture(t)
	sign_in(t, second)
	with_catalog(second)
	second.app.analysis_networks_path = path
	with_network(second, "hashed-a")
	with_clock(second, now.Add(time.Hour))
	if _, _, err := second.app.begin_analysis(context.Background(), true); !errors.Is(err, err_analysis_not_due) {
		t.Fatalf("an hour after a run, in a new process: %v, want not due", err)
	}
	with_clock(second, now.Add(25*time.Hour))
	_, flight, err := second.app.begin_analysis(context.Background(), true)
	if err != nil || flight == nil {
		t.Fatalf("a day after a run, in a new process: %v, want a run", err)
	}
	// The run writes into the temporary directory; it must end before the
	// directory is removed.
	<-flight.done
}

func Test_a_missing_or_damaged_networks_file_starts_empty_and_is_not_quoted(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	good, _ := json.Marshal(map[string]time.Time{"keep": now.Add(-time.Hour), "old": now.Add(-40 * 24 * time.Hour)})
	cases := []struct {
		name     string
		contents []byte // nil: no file
		want     map[string]bool
		warning  string
	}{
		{"no file", nil, map[string]bool{}, ""},
		{"a good file, pruned", good, map[string]bool{"keep": true}, ""},
		{"not json", []byte("SECRET-NOT-JSON"), map[string]bool{}, "analysis networks file invalid"},
		{"the wrong shape", []byte(`["SECRET-A","SECRET-B"]`), map[string]bool{}, "analysis networks file invalid"},
		{"a time that is not a time", []byte(`{"SECRET-KEY":"yesterday"}`), map[string]bool{}, "analysis networks file invalid"},
		{"an empty file", []byte{}, map[string]bool{}, "analysis networks file invalid"},
		{"null", []byte("null"), map[string]bool{}, ""},
		{"too large", bytes.Repeat([]byte(" SECRET"), analysis_networks_limit), map[string]bool{}, "analysis networks file unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := new_fixture(t)
			logs := logging(f)
			with_clock(f, now)
			path := filepath.Join(t.TempDir(), "analysis-networks.json")
			f.app.analysis_networks_path = path
			if c.contents != nil {
				if err := os.WriteFile(path, c.contents, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			f.app.analysis.mu.Lock()
			f.app.restore_networks_locked()
			got := map[string]bool{}
			for key := range f.app.analysis.networks {
				got[key] = true
			}
			f.app.analysis.mu.Unlock()

			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("networks = %v, want %v", got, c.want)
			}
			if c.warning == "" && strings.Contains(logs.String(), "level=WARN") {
				t.Fatalf("unexpected warning: %s", logs.String())
			}
			if c.warning != "" && !strings.Contains(logs.String(), c.warning) {
				t.Fatalf("log = %q, want a warning %q", logs.String(), c.warning)
			}
			if strings.Contains(logs.String(), "SECRET") {
				t.Fatalf("the log quotes the file: %s", logs.String())
			}
		})
	}
}

func Test_the_logs_of_an_automatic_run_are_categories_only(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	logs := logging(f)
	with_network(f, "SECRET-HASHED-KEY")
	f.control_plane.catalog_err = errors.New("SECRET-CONTROL-PLANE-TEXT")
	ch, unsubscribe := f.bus.Subscribe()
	defer unsubscribe()

	f.app.analyze_if_due(context.Background(), analysis_trigger_network_changed)
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "analysis failed") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case event := <-ch:
		t.Fatalf("a failed run published %s", event.Kind)
	default:
	}
	if !strings.Contains(logs.String(), "analysis failed") || !strings.Contains(logs.String(), "trigger=network_changed") {
		t.Fatalf("log = %q, want the failed run with its trigger", logs.String())
	}
	if strings.Contains(logs.String(), "SECRET") {
		t.Fatalf("the log carries more than categories: %s", logs.String())
	}
}

// --- the triggers that no network change asks for --------------------------

func Test_the_service_start_triggers_a_check(t *testing.T) {
	set_analysis_timers(t, time.Hour, 20*time.Millisecond, time.Hour)
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	with_network(f, "a")
	ch, unsubscribe := f.bus.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go f.app.watch_analysis(ctx)
	wait_for_analysis_event(t, ch)
	if got := f.reach_prober.calls.Load(); got != 1 {
		t.Fatalf("probes after the start = %d, want 1", got)
	}
}

func Test_the_connection_going_down_triggers_a_check_after_it_settles(t *testing.T) {
	set_analysis_timers(t, time.Hour, time.Hour, 20*time.Millisecond)
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	with_network(f, "a")
	connect(t, f)
	ch, unsubscribe := f.bus.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.app.watch_analysis(ctx)
	time.Sleep(50 * time.Millisecond) // let the watcher subscribe

	if got := f.reach_prober.calls.Load(); got != 0 {
		t.Fatalf("probes while connected = %d, want none", got)
	}
	if err := f.app.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	wait_for_analysis_event(t, ch)
	if got := f.reach_prober.calls.Load(); got != 1 {
		t.Fatalf("probes after the connection went down = %d, want 1", got)
	}
}

func Test_a_connection_that_comes_back_before_the_settle_is_not_measured(t *testing.T) {
	set_analysis_timers(t, time.Hour, time.Hour, 300*time.Millisecond)
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	with_network(f, "a")
	connect(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.app.watch_analysis(ctx)
	time.Sleep(50 * time.Millisecond)

	if err := f.app.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	connect(t, f)
	time.Sleep(450 * time.Millisecond)
	if got := f.reach_prober.calls.Load(); got != 0 {
		t.Fatalf("probes = %d, want none: the connection was up again when the settle ended", got)
	}
}

func Test_the_periodic_check_measures_a_machine_that_stays_on_one_network_daily(t *testing.T) {
	set_analysis_timers(t, 20*time.Millisecond, time.Hour, time.Hour)
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	with_network(f, "a")
	clock := with_clock(f, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	ch, unsubscribe := f.bus.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go f.app.watch_analysis(ctx)
	wait_for_analysis_event(t, ch)
	// Many checks pass, and none of them is due.
	time.Sleep(100 * time.Millisecond)
	if got := f.reach_prober.calls.Load(); got != 1 {
		t.Fatalf("probes the first day = %d, want 1", got)
	}

	clock.advance(25 * time.Hour)
	wait_for_analysis_event(t, ch)
	if got := f.reach_prober.calls.Load(); got != 2 {
		t.Fatalf("probes the second day = %d, want 2", got)
	}
}
