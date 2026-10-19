package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/reach"
)

// A catalog with a control inside the country and one host that is blocked
// at the handshake, and the results that go with it.
func with_catalog(f *fixture) {
	f.control_plane.catalog = api.Reach_catalog{Country: "RU", Targets: []api.Reach_target{
		{Key: "bank_ru", Host: "bank.example", Port: 443, Country: "RU", Label: "Bank"},
		{Key: "web_youtube", Host: "www.youtube.com", Port: 443, Label: "YouTube"},
	}}
	bank := reach.Target{Key: "bank_ru", Host: "bank.example", Port: 443, Country: "RU", Label: "Bank"}
	youtube := reach.Target{Key: "web_youtube", Host: "www.youtube.com", Port: 443, Label: "YouTube"}
	f.reach_prober.results = []reach.Result{
		{Target: bank, Dns: reach.Step_open, Tcp: reach.Step_open, Tls: reach.Step_open},
		{Target: youtube, Dns: reach.Step_open, Tcp: reach.Step_open, Tls: reach.Step_blocked, Failure: reach.Failure_reset},
	}
}

func Test_an_analysis_needs_a_signed_in_device(t *testing.T) {
	f := new_fixture(t)
	with_catalog(f)
	if _, err := f.app.Analyze(context.Background()); !errors.Is(err, ipc.Err_not_signed_in) {
		t.Fatalf("Analyze() before sign-in = %v, want not signed in", err)
	}
	if f.reach_prober.calls.Load() != 0 || f.control_plane.catalog_calls.Load() != 0 {
		t.Fatal("an analysis without a device token reached for the catalog or the network")
	}
}

func Test_an_analysis_is_judged_and_reported_with_the_catalog_country(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	got, err := f.app.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if got.Country != "RU" || got.Control != "ok" || got.Measured_at.IsZero() || len(got.Checks) != 2 {
		t.Fatalf("Analyze() = %+v", got)
	}
	bank, youtube := got.Checks[0], got.Checks[1]
	if !bank.Control || bank.Verdict != "open" || bank.Method != "" || bank.Failure != "" {
		t.Errorf("control check = %+v", bank)
	}
	if youtube.Control || youtube.Verdict != "blocked" || youtube.Method != "tls" || youtube.Failure != "reset" ||
		youtube.Dns != "open" || youtube.Tcp != "open" || youtube.Tls != "blocked" || youtube.Label != "YouTube" || youtube.Host != "www.youtube.com" {
		t.Errorf("blocked check = %+v", youtube)
	}
}

func Test_an_analysis_is_refused_while_a_tunnel_is_up(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	connect(t, f)
	if _, err := f.app.Analyze(context.Background()); !errors.Is(err, ipc.Err_tunnel_up) {
		t.Fatalf("Analyze() while connected = %v, want tunnel_up", err)
	}
	if f.reach_prober.calls.Load() != 0 {
		t.Fatal("the network was probed through a tunnel")
	}
	if err := f.app.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Analyze(context.Background()); err != nil {
		t.Fatalf("Analyze() after disconnecting = %v, want it to run", err)
	}
}

// start_blocked_analysis starts an Analyze that stops inside the prober and
// returns what it will answer.
func start_blocked_analysis(t *testing.T, f *fixture) <-chan error {
	t.Helper()
	f.reach_prober.enter = make(chan struct{}, 4)
	f.reach_prober.release = make(chan struct{})
	answer := make(chan error, 1)
	go func() {
		_, err := f.app.Analyze(context.Background())
		answer <- err
	}()
	select {
	case <-f.reach_prober.enter:
	case <-time.After(2 * time.Second):
		t.Fatal("the analysis never reached the prober")
	}
	return answer
}

func await_answer(t *testing.T, answer <-chan error) error {
	t.Helper()
	select {
	case err := <-answer:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("the analysis never answered")
		return nil
	}
}

func Test_a_connection_that_moves_during_an_analysis_discards_it(t *testing.T) {
	cases := []struct {
		name string
		move func(t *testing.T, f *fixture)
	}{
		{"a connect starts", func(t *testing.T, f *fixture) { connect(t, f) }},
		{"a connect comes and goes before the run ends", func(t *testing.T, f *fixture) {
			connect(t, f)
			if err := f.app.Disconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := new_fixture(t)
			sign_in(t, f)
			with_catalog(f)
			answer := start_blocked_analysis(t, f)

			c.move(t, f)
			close(f.reach_prober.release)

			if err := await_answer(t, answer); !errors.Is(err, ipc.Err_tunnel_up) {
				t.Fatalf("Analyze() = %v, want tunnel_up", err)
			}
			if last, _ := f.app.Analysis(context.Background()); last.Present {
				t.Fatalf("a discarded run was kept: %+v", last)
			}
		})
	}
}

func Test_a_second_analyze_during_a_run_waits_for_it_and_gets_the_same_result(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	first := start_blocked_analysis(t, f)

	var second_result ipc.Analysis_result
	var second_err error
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		second_result, second_err = f.app.Analyze(context.Background())
	}()
	// Give the second request time to arrive and join; it must not probe.
	time.Sleep(50 * time.Millisecond)
	close(f.reach_prober.release)

	if err := await_answer(t, first); err != nil {
		t.Fatalf("first Analyze() = %v", err)
	}
	wait.Wait()
	if second_err != nil || len(second_result.Checks) != 2 {
		t.Fatalf("second Analyze() = %+v, %v", second_result, second_err)
	}
	if calls := f.reach_prober.calls.Load(); calls != 1 {
		t.Fatalf("the network was probed %d times; want once for both", calls)
	}
}

func Test_a_run_within_a_minute_of_the_last_returns_it_and_a_later_one_probes_again(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f.app.now = func() time.Time { return now }

	first, err := f.app.Analyze(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Second)
	again, err := f.app.Analyze(context.Background())
	if err != nil || !again.Measured_at.Equal(first.Measured_at) || f.reach_prober.calls.Load() != 1 {
		t.Fatalf("Analyze() at 59 s = %+v, %v after %d probes; want the first result, not a new run", again, err, f.reach_prober.calls.Load())
	}

	now = now.Add(2 * time.Second)
	later, err := f.app.Analyze(context.Background())
	if err != nil || !later.Measured_at.Equal(now) || f.reach_prober.calls.Load() != 2 {
		t.Fatalf("Analyze() at 61 s = %+v, %v after %d probes; want a new run", later, err, f.reach_prober.calls.Load())
	}
}

func Test_a_failed_run_is_not_kept_and_the_next_one_probes_again(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	f.control_plane.catalog_err = &api.Error{Code: api.Code_unauthorized}
	if _, err := f.app.Analyze(context.Background()); !errors.Is(err, ipc.Err_unauthorized) {
		t.Fatalf("Analyze() with the catalog refused = %v, want unauthorized", err)
	}
	if last, _ := f.app.Analysis(context.Background()); last.Present {
		t.Fatalf("a failed run was kept: %+v", last)
	}
	f.control_plane.catalog_err = nil
	if _, err := f.app.Analyze(context.Background()); err != nil {
		t.Fatalf("Analyze() after the failure = %v", err)
	}
}

func Test_the_last_analysis_is_absent_until_a_run_finishes_and_is_not_a_probe(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	if last, err := f.app.Analysis(context.Background()); err != nil || last.Present || last.Result != nil {
		t.Fatalf("Analysis() before any run = %+v, %v; want nothing present", last, err)
	}
	if f.reach_prober.calls.Load() != 0 {
		t.Fatal("asking for the last analysis probed the network")
	}
	ran, err := f.app.Analyze(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last, err := f.app.Analysis(context.Background())
	if err != nil || !last.Present || last.Result == nil || !last.Result.Measured_at.Equal(ran.Measured_at) || len(last.Result.Checks) != 2 {
		t.Fatalf("Analysis() after a run = %+v, %v; want that run", last, err)
	}
	if f.reach_prober.calls.Load() != 1 {
		t.Fatalf("the network was probed %d times; want once", f.reach_prober.calls.Load())
	}
}

// wait_for_analysis_event waits for the push a finished background analysis
// makes, so a test does not read the probe count before the run is done.
func wait_for_analysis_event(t *testing.T, ch <-chan events.Event) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-ch:
			if event.Kind == events.Kind_diagnostic_completed {
				return
			}
		case <-deadline:
			t.Fatal("the background analysis never finished")
		}
	}
}

func Test_a_network_change_with_the_tunnel_down_analyses_once_per_five_minutes(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f.app.now = func() time.Time { return now }
	ch, unsubscribe := f.bus.Subscribe()
	defer unsubscribe()

	f.app.Network_changed(context.Background())
	wait_for_analysis_event(t, ch)
	if got := f.reach_prober.calls.Load(); got != 1 {
		t.Fatalf("probes after the first change = %d, want 1", got)
	}
	if last, _ := f.app.Analysis(context.Background()); !last.Present {
		t.Fatal("the background run left no analysis behind")
	}

	now = now.Add(2 * time.Minute)
	f.app.Network_changed(context.Background())
	time.Sleep(50 * time.Millisecond)
	if got := f.reach_prober.calls.Load(); got != 1 {
		t.Fatalf("probes 2 minutes later = %d, want still 1", got)
	}

	now = now.Add(4 * time.Minute)
	f.app.Network_changed(context.Background())
	wait_for_analysis_event(t, ch)
	if got := f.reach_prober.calls.Load(); got != 2 {
		t.Fatalf("probes 6 minutes after the first = %d, want 2", got)
	}
}

func Test_a_failed_background_analysis_still_spaces_the_next_one(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	f.reach_prober.err = errors.New("no network")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f.app.now = func() time.Time { return now }

	f.app.Network_changed(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for f.reach_prober.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)

	now = now.Add(2 * time.Minute)
	f.app.Network_changed(context.Background())
	time.Sleep(50 * time.Millisecond)
	if got := f.reach_prober.calls.Load(); got != 1 {
		t.Fatalf("probes = %d, want 1: a failed run still counts as started", got)
	}
}

func Test_a_network_change_without_a_sign_in_analyses_nothing(t *testing.T) {
	f := new_fixture(t)
	with_catalog(f)

	f.app.Network_changed(context.Background())
	time.Sleep(50 * time.Millisecond)
	if got := f.reach_prober.calls.Load(); got != 0 {
		t.Fatalf("probes = %d, want none without a device token", got)
	}
}

func Test_a_network_change_with_the_tunnel_up_takes_the_manager_path_and_does_not_analyse(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	connect(t, f)
	probes := f.prober.calls.Load()

	f.app.Network_changed(context.Background())

	if got := f.prober.calls.Load(); got != probes+1 {
		t.Fatalf("monitoring probes = %d, want one more than %d", got, probes)
	}
	if got := f.reach_prober.calls.Load(); got != 0 {
		t.Fatalf("the network was analysed %d times through a tunnel", got)
	}
}

func Test_a_network_change_right_after_a_disconnect_is_the_tunnel_going_away(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	connect(t, f)
	if err := f.app.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	ch, unsubscribe := f.bus.Subscribe()
	defer unsubscribe()

	// The adapter's own removal, a moment after the disconnect.
	f.app.Network_changed(context.Background())
	time.Sleep(50 * time.Millisecond)
	if got := f.reach_prober.calls.Load(); got != 0 {
		t.Fatalf("probes right after a disconnect = %d, want none", got)
	}

	later := f.app.now().Add(network_change_after_disconnect + time.Second)
	f.app.now = func() time.Time { return later }
	f.app.Network_changed(context.Background())
	wait_for_analysis_event(t, ch)
	if got := f.reach_prober.calls.Load(); got != 1 {
		t.Fatalf("probes once the disconnect has settled = %d, want 1", got)
	}
}
