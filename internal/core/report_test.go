package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"singularity-vpn-app/internal/buildinfo"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/journal"
)

// report_control_plane is the fake control plane that also takes feedback,
// and records how.
type report_control_plane struct {
	*fake_control_plane

	mu           sync.Mutex
	plain        []ipc.Feedback_params
	reports      []ipc.Feedback_params
	diagnostics  []json.RawMessage
	logs         []string
	tokens       []string
	report_error error

	// When release is set, Report_problem tells entered it has begun and
	// waits for release to close: a send that is in flight.
	entered chan struct{}
	release chan struct{}
}

func (c *report_control_plane) Feedback(_ context.Context, token string, params ipc.Feedback_params) (ipc.Feedback_result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plain = append(c.plain, params)
	return ipc.Feedback_result{Accepted: true, Code: "R-PLAIN"}, nil
}

func (c *report_control_plane) Report_problem(_ context.Context, token string, params ipc.Feedback_params, diagnostics json.RawMessage, log string) (ipc.Feedback_result, error) {
	if c.release != nil {
		c.entered <- struct{}{}
		<-c.release
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.report_error != nil {
		return ipc.Feedback_result{}, c.report_error
	}
	c.tokens = append(c.tokens, token)
	c.reports = append(c.reports, params)
	c.diagnostics = append(c.diagnostics, diagnostics)
	c.logs = append(c.logs, log)
	return ipc.Feedback_result{Accepted: true, Code: "R-TEST"}, nil
}

// report_fixture is a signed-in application whose clock the test holds, with
// a journal and a log to attach.
type report_fixture struct {
	*fixture
	plane   *report_control_plane
	journal *journal.Journal
	logger  slog.Handler
	now     time.Time
	log     string
}

func new_report_fixture(t *testing.T) *report_fixture {
	t.Helper()
	f := &report_fixture{fixture: new_fixture(t), now: log_now}
	f.plane = &report_control_plane{fake_control_plane: f.control_plane}
	f.app.control_plane = f.plane
	f.app.now = func() time.Time { return f.now }
	f.journal = journal.New(500)
	f.logger = journal.Handler(slog.DiscardHandler, f.journal)
	f.app.journal = f.journal

	f.log = filepath.Join(t.TempDir(), "service.log")
	write_log(t, f.log, log_line(time.Hour, "service starting"), log_line(time.Minute, "connect requested token=abc"))
	f.app.log_path = f.log
	sign_in(t, f.fixture)
	return f
}

// record puts an entry in the journal at d before the fixture's clock.
func (f *report_fixture) record(t *testing.T, d time.Duration, message string, attrs ...slog.Attr) {
	t.Helper()
	record := slog.NewRecord(f.now.Add(-d), slog.LevelInfo, message, 0)
	record.AddAttrs(attrs...)
	if err := f.logger.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

func decode_diagnostics(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var bundle map[string]json.RawMessage
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("the diagnostics are not a JSON object: %v", err)
	}
	return bundle
}

func Test_a_draft_is_the_bundle_the_control_plane_reads(t *testing.T) {
	old_version := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = old_version })
	buildinfo.Version = "0.1.4-dev.abc1234"

	f := new_report_fixture(t)
	f.app.os_version = "10.0.26300"
	f.app.network_summary = func(context.Context) (any, error) {
		return []map[string]any{{"name": "Wi-Fi", "kind": "wifi", "ipv4": []string{"private"}}}, nil
	}
	f.record(t, 3*time.Hour, "strategy set") // outside the two hours
	f.record(t, time.Hour, "event", slog.String("kind", "connection_blip"))
	f.record(t, time.Minute, "verification failed", slog.String("token", "abc"), slog.String("error", "refused: Bearer xyz"))
	f.record(t, 30*time.Second, "event", slog.String("kind", "connection_lost"))
	apps := []ipc.App_use{{Exe: "cs2.exe", From: f.now.Add(-time.Hour), To: f.now.Add(-time.Minute)}}

	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{Apps: apps})
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Id) != 32 || strings.Trim(draft.Id, "0123456789abcdef") != "" {
		t.Fatalf("draft id %q is not 32 lowercase hex", draft.Id)
	}
	if len(draft.Diagnostics) > diagnostics_max_bytes {
		t.Fatalf("diagnostics are %d bytes", len(draft.Diagnostics))
	}
	bundle := decode_diagnostics(t, draft.Diagnostics)
	var text string
	var number float64
	for key, want := range map[string]string{"app_version": "0.1.4-dev.abc1234", "os": "10.0.26300", "tunnel_mode": "vpn"} {
		if json.Unmarshal(bundle[key], &text) != nil || text != want {
			t.Errorf("%s = %s, want %q", key, bundle[key], want)
		}
	}
	if json.Unmarshal(bundle["schema"], &number) != nil || number != 1 {
		t.Errorf("schema = %s", bundle["schema"])
	}
	var created time.Time
	if json.Unmarshal(bundle["created_at"], &created) != nil || !created.Equal(f.now) {
		t.Errorf("created_at = %s", bundle["created_at"])
	}
	for _, key := range []string{"status", "strategy", "network", "apps", "journal", "failures", "connection"} {
		if _, ok := bundle[key]; !ok && key != "failures" {
			t.Errorf("the bundle has no %q", key)
		}
	}

	// A machine with no tunnel names no node and no session, and its mode
	// is the stored choice.
	var connection map[string]any
	if err := json.Unmarshal(bundle["connection"], &connection); err != nil {
		t.Fatal(err)
	}
	if connection["state"] != "disconnected" || connection["mode"] != "full" {
		t.Errorf("connection = %v", connection)
	}
	for _, key := range []string{"node_id", "protocol", "session_id"} {
		if _, ok := connection[key]; ok {
			t.Errorf("connection names a %s without a session: %v", key, connection)
		}
	}

	var entries []journal.Entry
	if err := json.Unmarshal(bundle["journal"], &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("journal has %d entries, want the 3 of the last two hours: %v", len(entries), entries)
	}
	failed := entries[1]
	if failed.Message != "verification failed" || failed.Attrs["token"] != redacted || failed.Attrs["error"] != "refused: Bearer "+redacted {
		t.Errorf("secrets left the journal: %+v", failed)
	}
	if own := f.journal.List()[2]; own.Attrs["token"] != "abc" {
		t.Errorf("the bundle changed the journal itself: %+v", own)
	}

	var sent_apps []ipc.App_use
	if err := json.Unmarshal(bundle["apps"], &sent_apps); err != nil || len(sent_apps) != 1 || sent_apps[0].Exe != "cs2.exe" {
		t.Errorf("apps = %s", bundle["apps"])
	}
	var network []map[string]any
	if err := json.Unmarshal(bundle["network"], &network); err != nil || len(network) != 1 || network[0]["name"] != "Wi-Fi" {
		t.Errorf("network = %s", bundle["network"])
	}

	// The log tail is held, redacted, and described to the window.
	if draft.Log_bytes == 0 || draft.Log_from == nil || !draft.Log_from.Equal(f.now.Add(-time.Hour)) {
		t.Errorf("log_bytes = %d, log_from = %v", draft.Log_bytes, draft.Log_from)
	}
	held, ok := f.app.drafts.get(draft.Id, f.now)
	if !ok || len(held.log) != draft.Log_bytes || strings.Contains(held.log, "token=abc") || !strings.Contains(held.log, "token="+redacted) {
		t.Errorf("held log = %q", held.log)
	}
	if draft.Log != held.log {
		t.Errorf("the log shown is not the log held:\nshown %q\nheld  %q", draft.Log, held.log)
	}
	if string(held.diagnostics) != string(draft.Diagnostics) {
		t.Error("what is held is not what was shown")
	}
}

func Test_a_draft_names_the_connection_the_status_names(t *testing.T) {
	f := new_report_fixture(t)
	connect(t, f.fixture)
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Connection map[string]string `json:"connection"`
		Status     ipc.Status_result `json:"status"`
		Apps       []ipc.App_use     `json:"apps"`
	}
	if err := json.Unmarshal(draft.Diagnostics, &bundle); err != nil {
		t.Fatal(err)
	}
	status, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Session_id == "" || status.Node_id == "" {
		t.Fatalf("the fixture has no session: %+v", status)
	}
	want := map[string]string{"state": "connected", "node_id": status.Node_id, "protocol": status.Protocol, "mode": "full", "session_id": status.Session_id}
	for key, value := range want {
		if bundle.Connection[key] != value {
			t.Errorf("connection.%s = %q, want %q", key, bundle.Connection[key], value)
		}
	}
	if bundle.Status.Session_id != status.Session_id || bundle.Status.State != "connected" {
		t.Errorf("status = %+v", bundle.Status)
	}
	if bundle.Apps == nil {
		t.Error("no apps should be an empty list, not a missing one")
	}
}

func Test_a_draft_leaves_out_what_it_does_not_know(t *testing.T) {
	old_version := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = old_version })
	buildinfo.Version = ""

	f := new_report_fixture(t)
	f.app.log_path = ""
	f.app.network_summary = func(context.Context) (any, error) { return nil, errors.New("no adapters") }
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	bundle := decode_diagnostics(t, draft.Diagnostics)
	for _, key := range []string{"app_version", "os", "network_context_id", "network", "failures"} {
		if _, ok := bundle[key]; ok {
			t.Errorf("the bundle names a %s it was not given: %s", key, bundle[key])
		}
	}
	if draft.Log_bytes != 0 || draft.Log_from != nil {
		t.Errorf("a log that is not there was described: %+v", draft)
	}

	// A summary that is a typed nil slice is nothing, too.
	f.app.network_summary = func(context.Context) (any, error) { return []string(nil), nil }
	draft, err = f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decode_diagnostics(t, draft.Diagnostics)["network"]; ok {
		t.Error("an empty network summary was sent as null")
	}
}

func Test_a_draft_carries_the_failure_signal(t *testing.T) {
	f := new_report_fixture(t)
	f.record(t, 5*time.Minute, "event", slog.String("kind", "connection_lost"))
	f.record(t, time.Minute, "event", slog.String("kind", "connection_blip"))
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Failures *ipc.Failure_signal `json:"failures"`
		Status   ipc.Status_result   `json:"status"`
	}
	if err := json.Unmarshal(draft.Diagnostics, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Failures == nil || bundle.Failures.Count != 2 || bundle.Status.Failures != nil {
		t.Fatalf("failures = %+v, status.failures = %+v: the signal is the bundle's own field, once", bundle.Failures, bundle.Status.Failures)
	}
}

func Test_status_carries_the_failure_signal_even_without_a_session(t *testing.T) {
	f := new_report_fixture(t)
	status, err := f.app.Status(context.Background())
	if err != nil || status.Failures != nil {
		t.Fatalf("status.failures = %+v, %v", status.Failures, err)
	}
	f.record(t, 5*time.Minute, "event", slog.String("kind", "connection_lost"))
	f.record(t, time.Minute, "event", slog.String("kind", "connection_blip"))
	status, err = f.app.Status(context.Background())
	if err != nil || status.Failures == nil || status.Failures.Count != 2 || status.Failures.Lost != 1 || status.Failures.Blips != 1 {
		t.Fatalf("status.failures = %+v, %v", status.Failures, err)
	}
}

func Test_a_draft_is_sent_as_shown_and_forgotten_once_accepted(t *testing.T) {
	f := new_report_fixture(t)
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	held, _ := f.app.drafts.get(draft.Id, f.now)

	params := ipc.Feedback_params{Kind: "problem", Message: "It keeps dropping", Draft: draft.Id}
	result, err := f.app.Feedback(context.Background(), params)
	if err != nil || !result.Accepted || result.Code != "R-TEST" {
		t.Fatalf("feedback = %+v, %v", result, err)
	}
	if len(f.plane.reports) != 1 || len(f.plane.plain) != 0 {
		t.Fatalf("reports = %d, plain = %d", len(f.plane.reports), len(f.plane.plain))
	}
	if string(f.plane.diagnostics[0]) != string(draft.Diagnostics) || f.plane.logs[0] != held.log || f.plane.logs[0] == "" {
		t.Error("what was sent is not what was shown")
	}
	if f.plane.tokens[0] != "device-token" {
		t.Errorf("sent with token %q", f.plane.tokens[0])
	}

	// Once accepted the draft is gone: pressing send twice sends once.
	if _, err := f.app.Feedback(context.Background(), params); err == nil || err.Error() != "the report draft expired; open it again" {
		t.Fatalf("second send: %v", err)
	}
	if len(f.plane.reports) != 1 {
		t.Fatal("the report was sent twice")
	}
}

func Test_a_draft_that_failed_to_send_stays_for_another_try(t *testing.T) {
	f := new_report_fixture(t)
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	params := ipc.Feedback_params{Kind: "problem", Message: "m", Draft: draft.Id}
	f.plane.report_error = errors.New("offline")
	if _, err := f.app.Feedback(context.Background(), params); err == nil {
		t.Fatal("a failed send was reported as sent")
	}
	f.plane.report_error = nil
	if _, err := f.app.Feedback(context.Background(), params); err != nil {
		t.Fatalf("the retry: %v", err)
	}
}

func Test_feedback_without_a_draft_is_unchanged(t *testing.T) {
	f := new_report_fixture(t)
	result, err := f.app.Feedback(context.Background(), ipc.Feedback_params{Kind: "problem", Message: "m"})
	if err != nil || result.Code != "R-PLAIN" || len(f.plane.plain) != 1 || len(f.plane.reports) != 0 {
		t.Fatalf("feedback = %+v, %v; plain = %d, reports = %d", result, err, len(f.plane.plain), len(f.plane.reports))
	}
}

func Test_an_unknown_draft_is_expired(t *testing.T) {
	f := new_report_fixture(t)
	_, err := f.app.Feedback(context.Background(), ipc.Feedback_params{Kind: "problem", Message: "m", Draft: strings.Repeat("a", 32)})
	if err == nil || err.Error() != "the report draft expired; open it again" {
		t.Fatalf("error = %v", err)
	}
	if len(f.plane.reports)+len(f.plane.plain) != 0 {
		t.Fatal("something was sent for a draft that does not exist")
	}
}

func Test_a_draft_expires_after_fifteen_minutes(t *testing.T) {
	f := new_report_fixture(t)
	fresh, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}

	f.now = f.now.Add(draft_lifetime)
	if _, err := f.app.Feedback(context.Background(), ipc.Feedback_params{Kind: "problem", Message: "m", Draft: fresh.Id}); err != nil {
		t.Fatalf("a draft on its fifteenth minute: %v", err)
	}
	f.now = f.now.Add(time.Second)
	if _, err := f.app.Feedback(context.Background(), ipc.Feedback_params{Kind: "problem", Message: "m", Draft: stale.Id}); err == nil || err.Error() != "the report draft expired; open it again" {
		t.Fatalf("a draft past it: %v", err)
	}
}

func Test_only_the_four_newest_drafts_are_kept(t *testing.T) {
	f := new_report_fixture(t)
	var ids []string
	for i := 0; i < 5; i++ {
		draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, draft.Id)
		f.now = f.now.Add(time.Second)
	}
	if _, ok := f.app.drafts.get(ids[0], f.now); ok {
		t.Error("the oldest of five drafts is still held")
	}
	for _, id := range ids[1:] {
		if _, ok := f.app.drafts.get(id, f.now); !ok {
			t.Errorf("draft %s was dropped though it is among the newest four", id)
		}
	}
	if len(f.app.drafts.items) != 4 {
		t.Errorf("%d drafts held", len(f.app.drafts.items))
	}
}

func Test_draft_ids_do_not_repeat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := new_draft_id()
		if err != nil || len(id) != 32 || seen[id] {
			t.Fatalf("id %q, %v", id, err)
		}
		seen[id] = true
	}
}

func Test_a_draft_refuses_programs_that_are_paths(t *testing.T) {
	f := new_report_fixture(t)
	_, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{Apps: []ipc.App_use{{Exe: `C:\Users\someone\cs2.exe`}}})
	if err == nil {
		t.Fatal("a path was accepted as a program name")
	}
}

func Test_diagnostics_fit_by_losing_the_oldest_journal_entries(t *testing.T) {
	var entries []journal.Entry
	for i := 0; i < 2000; i++ {
		entries = append(entries, journal.Entry{
			At: log_now.Add(time.Duration(i) * time.Second), Level: "info", Message: "event",
			Attrs: map[string]string{"kind": "connection_blip", "n": strings.Repeat("x", i%7) + string(rune('a'+i%26))},
		})
	}
	whole, err := json.Marshal(entries)
	if err != nil || len(whole) < 2*diagnostics_max_bytes {
		t.Fatalf("the test journal is only %d bytes", len(whole))
	}
	raw, err := fit_diagnostics(problem_diagnostics{Schema: 1, Created_at: log_now, Journal: entries}, diagnostics_max_bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > diagnostics_max_bytes {
		t.Fatalf("diagnostics are %d bytes, over %d", len(raw), diagnostics_max_bytes)
	}
	var got problem_diagnostics
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Journal) == 0 || len(got.Journal) >= len(entries) {
		t.Fatalf("kept %d of %d", len(got.Journal), len(entries))
	}
	// The newest are what is kept, whole and in order.
	offset := len(entries) - len(got.Journal)
	for i, entry := range got.Journal {
		if !entry.At.Equal(entries[offset+i].At) {
			t.Fatalf("entry %d is not the %dth of the journal", i, offset+i)
		}
	}
	// And as many as fit: one more would not have.
	more := problem_diagnostics{Schema: 1, Created_at: log_now, Journal: entries[offset-1:]}
	if longer, _ := json.Marshal(more); len(longer) <= diagnostics_max_bytes {
		t.Fatalf("an entry that fits was dropped (%d bytes with it)", len(longer))
	}
}

func Test_diagnostics_that_fit_are_kept_whole_and_an_empty_journal_is_a_list(t *testing.T) {
	raw, err := fit_diagnostics(problem_diagnostics{Schema: 1, Created_at: log_now}, diagnostics_max_bytes)
	if err != nil || !strings.Contains(string(raw), `"journal":[]`) {
		t.Fatalf("diagnostics = %s, %v", raw, err)
	}
	entries := []journal.Entry{{At: log_now, Level: "info", Message: "a"}, {At: log_now, Level: "info", Message: "b"}}
	raw, err = fit_diagnostics(problem_diagnostics{Schema: 1, Created_at: log_now, Journal: entries}, diagnostics_max_bytes)
	if err != nil || !strings.Contains(string(raw), `"a"`) || !strings.Contains(string(raw), `"b"`) {
		t.Fatalf("diagnostics = %s, %v", raw, err)
	}
}

func Test_diagnostics_too_big_without_a_journal_are_refused(t *testing.T) {
	big := json.RawMessage(`"` + strings.Repeat("x", 100) + `"`)
	if _, err := fit_diagnostics(problem_diagnostics{Schema: 1, Created_at: log_now, Network: big}, 100); err == nil {
		t.Fatal("diagnostics over the limit before the journal were cut and sent")
	}
}

func Test_a_draft_names_the_session_the_failures_happened_in(t *testing.T) {
	f := new_report_fixture(t)
	failing := []slog.Attr{
		slog.String("kind", "connection_blip"),
		slog.String("session_id", "27d09bc16e421d8933137a3c967c4169"),
		slog.String("node_id", "node-failing"),
		slog.String("protocol", "amneziawg"),
	}
	f.record(t, 20*time.Minute, "event", failing...)
	f.record(t, 15*time.Minute, "event", failing...)
	f.record(t, 10*time.Minute, "event", failing...)
	// The service restarted since, and holds a new, healthy session.
	connect(t, f.fixture)
	status, err := f.app.Status(context.Background())
	if err != nil || status.Session_id == "" || status.Session_id == "27d09bc16e421d8933137a3c967c4169" {
		t.Fatalf("the fixture has no session of its own: %+v, %v", status, err)
	}

	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Connection map[string]string `json:"connection"`
	}
	if err := json.Unmarshal(draft.Diagnostics, &bundle); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"session_id": "27d09bc16e421d8933137a3c967c4169", "node_id": "node-failing", "protocol": "amneziawg", "state": "connected"}
	for key, value := range want {
		if bundle.Connection[key] != value {
			t.Errorf("connection.%s = %q, want %q: the report is about the connection that failed", key, bundle.Connection[key], value)
		}
	}
}

// A report is about the failures it carries, not only those of the last half
// hour: a connection that broke an hour ago and was brought back by hand is
// what the person reports, not the healthy one that replaced it, nor the mode
// chosen since (R-ZWP9, 2026-10-28: sent 55 minutes after the loss, it named
// the connection made afterwards, in Antizapret).
func Test_a_draft_names_the_connection_that_broke_an_hour_ago(t *testing.T) {
	f := new_report_fixture(t)
	failing := []slog.Attr{
		slog.String("kind", "connection_lost"),
		slog.String("session_id", "5a65bb277d237fdfe01f64db1ccb04e0"),
		slog.String("node_id", "node-failing"),
		slog.String("protocol", "wireguard"),
		slog.String("tunnel", "vpn"),
	}
	f.record(t, 57*time.Minute, "event", failing...)
	f.record(t, 56*time.Minute, "event", failing...)
	if err := f.app.Set_tunnel_mode(context.Background(), ipc.Tunnel_mode{Mode: ipc.Tunnel_antizapret}); err != nil {
		t.Fatal(err)
	}
	connect(t, f.fixture)

	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Connection map[string]string `json:"connection"`
	}
	if err := json.Unmarshal(draft.Diagnostics, &bundle); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"session_id": "5a65bb277d237fdfe01f64db1ccb04e0", "node_id": "node-failing", "protocol": "wireguard", "mode": "full"}
	for key, value := range want {
		if bundle.Connection[key] != value {
			t.Errorf("connection.%s = %q, want %q: the report is about the connection that broke", key, bundle.Connection[key], value)
		}
	}
}

// send_draft makes a draft and sends it, and returns what was sent.
func (f *report_fixture) send_draft(t *testing.T) (journal_messages []string, log string) {
	t.Helper()
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Feedback(context.Background(), ipc.Feedback_params{Kind: "problem", Message: "It keeps dropping", Draft: draft.Id}); err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Journal []journal.Entry `json:"journal"`
	}
	if err := json.Unmarshal(f.plane.diagnostics[len(f.plane.diagnostics)-1], &bundle); err != nil {
		t.Fatal(err)
	}
	for _, entry := range bundle.Journal {
		journal_messages = append(journal_messages, entry.Message)
	}
	return journal_messages, f.plane.logs[len(f.plane.logs)-1]
}

func Test_a_report_does_not_send_again_what_an_earlier_one_sent(t *testing.T) {
	f := new_report_fixture(t)
	f.app.report_sent_path = filepath.Join(t.TempDir(), "report-sent.json")
	f.record(t, 10*time.Minute, "connect requested")
	journal_first, log_first := f.send_draft(t)
	if !slices.Contains(journal_first, "connect requested") || !strings.Contains(log_first, "service starting") {
		t.Fatalf("the first report did not carry what there was: %q, %q", journal_first, log_first)
	}

	f.now = f.now.Add(5 * time.Minute)
	f.record(t, time.Minute, "disconnect requested")
	write_log(t, f.log, log_line(time.Hour, "service starting"), log_line(time.Minute, "connect requested token=abc"), log_line(-4*time.Minute, "after the first report"))
	journal_second, log_second := f.send_draft(t)
	if slices.Contains(journal_second, "connect requested") || !slices.Contains(journal_second, "disconnect requested") {
		t.Errorf("second journal = %q: only what came after the first report", journal_second)
	}
	if strings.Contains(log_second, "service starting") || strings.Contains(log_second, "connect requested") || !strings.Contains(log_second, "after the first report") {
		t.Errorf("second log = %q: only what came after the first report", log_second)
	}
}

func Test_what_was_sent_is_remembered_across_a_restart(t *testing.T) {
	sent_path := filepath.Join(t.TempDir(), "report-sent.json")
	f := new_report_fixture(t)
	f.app.report_sent_path = sent_path
	f.send_draft(t)

	// A service started afresh: the same files, an empty journal.
	g := new_report_fixture(t)
	g.app.report_sent_path = sent_path
	g.app.log_path = f.log
	g.now = f.now.Add(time.Minute)
	if _, log := g.send_draft(t); log != "" {
		t.Errorf("after a restart the log went again: %q", log)
	}
}

func Test_a_report_that_failed_to_send_does_not_count_as_sent(t *testing.T) {
	f := new_report_fixture(t)
	f.app.report_sent_path = filepath.Join(t.TempDir(), "report-sent.json")
	draft, err := f.app.Problem_draft(context.Background(), ipc.Problem_draft_params{})
	if err != nil {
		t.Fatal(err)
	}
	f.plane.report_error = errors.New("control plane unreachable")
	if _, err := f.app.Feedback(context.Background(), ipc.Feedback_params{Kind: "problem", Message: "x", Draft: draft.Id}); err == nil {
		t.Fatal("the send should have failed")
	}
	f.plane.report_error = nil
	f.now = f.now.Add(time.Minute)
	if _, log := f.send_draft(t); !strings.Contains(log, "service starting") {
		t.Errorf("log = %q: nothing went the first time, so all of it goes now", log)
	}
}
