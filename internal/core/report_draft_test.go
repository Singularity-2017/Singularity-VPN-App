package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/journal"
	"singularity-vpn-app/internal/state"
)

func draft_bundle(t *testing.T, f *report_fixture) (ipc.Problem_draft, problem_diagnostics) {
	t.Helper()
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	var bundle problem_diagnostics
	if err := json.Unmarshal(draft.Diagnostics, &bundle); err != nil {
		t.Fatal(err)
	}
	return draft, bundle
}

// A connection that never came up has no session, but the report still has to
// say what it was trying and in which mode observations would call it.
func Test_a_draft_without_a_session_takes_its_target_from_the_journal_and_its_mode_from_the_manager(t *testing.T) {
	f := new_report_fixture(t)
	f.record(t, 30*time.Minute, "verification failed", slog.String("node_id", "old-node"), slog.String("protocol", "openvpn"))
	f.record(t, 10*time.Minute, "connect requested", slog.String("mode", "auto"), slog.String("node_id", ""), slog.String("protocol", ""))
	f.record(t, 5*time.Minute, "verification failed", slog.String("node_id", "de-01"), slog.String("protocol", "wireguard"), slog.String("transport", "x"))
	f.record(t, time.Minute, "event", slog.String("kind", "connection_blip")) // names neither
	f.record(t, 30*time.Second, "service starting", slog.String("node_id", "only-a-node"))

	if err := f.store.Save(state.State{Tunnel_mode: "antizapret"}); err != nil {
		t.Fatal(err)
	}
	_, bundle := draft_bundle(t, f)
	got := bundle.Connection
	if got.Node_id != "de-01" || got.Protocol != "wireguard" {
		t.Errorf("target = %s over %s, want the newest entry that names both", got.Node_id, got.Protocol)
	}
	if got.Session_id != "" || got.State != "disconnected" {
		t.Errorf("connection = %+v", got)
	}
	if got.Mode != "selective" {
		t.Errorf("mode = %q; an observation would say selective", got.Mode)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "session_id") {
		t.Errorf("a session id was made up: %s", raw)
	}
}

func Test_a_draft_without_a_session_or_a_journal_names_no_target(t *testing.T) {
	f := new_report_fixture(t)
	_, bundle := draft_bundle(t, f)
	if bundle.Connection.Node_id != "" || bundle.Connection.Protocol != "" || bundle.Connection.Session_id != "" {
		t.Errorf("connection = %+v", bundle.Connection)
	}
	if bundle.Connection.Mode != "full" {
		t.Errorf("mode = %q", bundle.Connection.Mode)
	}
}

func Test_a_live_session_names_its_own_target_not_the_journals(t *testing.T) {
	f := new_report_fixture(t)
	f.record(t, time.Minute, "verification failed", slog.String("node_id", "elsewhere"), slog.String("protocol", "openvpn"))
	connect(t, f.fixture)
	_, bundle := draft_bundle(t, f)
	if bundle.Connection.Node_id != "node-1" || bundle.Connection.Protocol != "wireguard" || bundle.Connection.Session_id == "" {
		t.Errorf("connection = %+v", bundle.Connection)
	}
}

func Test_latest_target_wants_both_and_the_newest(t *testing.T) {
	entries := []journal.Entry{
		{Attrs: map[string]string{"node_id": "a", "protocol": "wireguard"}},
		{Attrs: map[string]string{"node_id": "b"}},
		{Attrs: map[string]string{"node_id": "", "protocol": ""}},
		{},
	}
	if node, protocol := latest_target(entries); node != "a" || protocol != "wireguard" {
		t.Fatalf("target = %q %q", node, protocol)
	}
	if node, protocol := latest_target(nil); node != "" || protocol != "" {
		t.Fatalf("target = %q %q", node, protocol)
	}
}

func Test_the_journal_in_a_draft_gets_the_whole_redactor(t *testing.T) {
	f := new_report_fixture(t)
	// A warning is kept whatever its message says, so its words are free text.
	warning := slog.NewRecord(f.now.Add(-time.Minute), slog.LevelWarn, "dial failed: token=abc123 via 2001:db8::1", 0)
	warning.AddAttrs(
		slog.String("error", "refused: Authorization: Basic dXNlcjpwYXNz at 203.0.113.9"),
		slog.String("psk", "pskvalue"))
	if err := f.logger.Handle(context.Background(), warning); err != nil {
		t.Fatal(err)
	}
	draft, bundle := draft_bundle(t, f)
	for _, leaked := range []string{"abc123", "dXNlcjpwYXNz", "pskvalue", "2001:db8", "203.0.113"} {
		if strings.Contains(string(draft.Diagnostics), leaked) {
			t.Errorf("%q is in the diagnostics", leaked)
		}
	}
	if len(bundle.Journal) != 1 || !strings.Contains(bundle.Journal[0].Message, "token="+redacted) {
		t.Errorf("journal = %+v", bundle.Journal)
	}
}

// A system call that stalls must cost the person the network section, not the
// report.
func Test_a_network_summary_that_stalls_does_not_hold_the_draft(t *testing.T) {
	f := new_report_fixture(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.app.network_summary = func(context.Context) (any, error) {
		<-release
		return []string{"too late"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan ipc.Problem_draft, 1)
	go func() {
		draft, err := f.app.Problem_draft(ctx, ipc.Problem_draft_params{})
		if err != nil {
			t.Error(err)
		}
		done <- draft
	}()
	select {
	case draft := <-done:
		if strings.Contains(string(draft.Diagnostics), "too late") || strings.Contains(string(draft.Diagnostics), `"network"`) {
			t.Errorf("a summary that did not arrive is in the bundle: %s", draft.Diagnostics)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the draft waited for a summary that never came")
	}
}

// Two presses of send, or two windows: only one report goes out.
func Test_two_sends_of_one_draft_post_once(t *testing.T) {
	f := new_report_fixture(t)
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	f.plane.entered = make(chan struct{})
	f.plane.release = make(chan struct{})
	params := ipc.Feedback_params{Kind: "problem", Message: "m", Draft: draft.Id}

	var wg sync.WaitGroup
	first_error := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := f.app.Feedback(context.Background(), params)
		first_error <- err
	}()
	<-f.plane.entered // the first send is in flight, holding the draft

	if _, err := f.app.Feedback(context.Background(), params); err == nil || err.Error() != "the report draft expired; open it again" {
		t.Errorf("a second send while the first is in flight: %v", err)
	}
	close(f.plane.release)
	wg.Wait()
	if err := <-first_error; err != nil {
		t.Fatalf("the first send: %v", err)
	}
	if len(f.plane.reports) != 1 {
		t.Fatalf("%d reports were posted", len(f.plane.reports))
	}
}

func Test_a_send_that_fails_before_posting_gives_the_draft_back(t *testing.T) {
	f := new_report_fixture(t)
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	f.app.control_plane = f.control_plane // one that takes no problem reports
	params := ipc.Feedback_params{Kind: "problem", Message: "m", Draft: draft.Id}
	if _, err := f.app.Feedback(context.Background(), params); err == nil {
		t.Fatal("a control plane that cannot take reports took one")
	}
	f.app.control_plane = f.plane
	if _, err := f.app.Feedback(context.Background(), params); err != nil {
		t.Fatalf("the draft was lost with the failed send: %v", err)
	}
}
