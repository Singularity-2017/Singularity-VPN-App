package journal

import (
	"bytes"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func logger_with(j *Journal) (*slog.Logger, *bytes.Buffer) {
	var out bytes.Buffer
	return slog.New(Handler(slog.NewTextHandler(&out, nil), j)), &out
}

func Test_operations_are_kept_and_churn_is_not(t *testing.T) {
	j := New(10)
	logger, out := logger_with(j)
	logger.Info("connect requested", "mode", "auto")
	logger.Info("game router: game ports", "ports", 28)
	logger.Info("event", "kind", "better_option_available")
	logger.Info("event", "kind", "connection_established", "session_id", "s1")
	logger.Warn("something new went wrong", "error", "boom")

	got := j.List()
	if len(got) != 3 {
		t.Fatalf("kept %d entries, want 3: %+v", len(got), got)
	}
	if got[0].Message != "connect requested" || got[0].Attrs["mode"] != "auto" {
		t.Errorf("first entry %+v", got[0])
	}
	if got[1].Attrs["kind"] != "connection_established" {
		t.Errorf("second entry %+v", got[1])
	}
	if got[2].Level != "warn" {
		t.Errorf("a warning not on the list was not kept: %+v", got[2])
	}
	if !strings.Contains(out.String(), "game router: game ports") {
		t.Error("the log itself lost a record the journal skipped")
	}
}

func Test_addresses_never_reach_the_journal(t *testing.T) {
	j := New(4)
	logger, _ := logger_with(j)
	logger.Warn("verification failed", "error", "dial tcp 203.0.113.7:443: timeout")
	if got := j.List()[0].Attrs["error"]; strings.Contains(got, "203.0.113.7") {
		t.Fatalf("an address reached the journal: %q", got)
	}
}

func Test_profile_and_network_changes_are_kept_once_without_identifiers(t *testing.T) {
	j := New(4)
	logger, _ := logger_with(j)
	for _, kind := range []string{"profile_changed", "network_changed"} {
		logger.Info("event", "kind", kind)
	}
	entries := j.List()
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want two semantic changes", entries)
	}
	for i, kind := range []string{"profile_changed", "network_changed"} {
		if entries[i].Attrs["kind"] != kind || len(entries[i].Attrs) != 1 {
			t.Fatalf("entry = %+v, want only kind %s", entries[i], kind)
		}
	}
}

func Test_probe_diagnostics_stay_in_log_and_connection_loss_stays_in_journal(t *testing.T) {
	j := New(10)
	logger, out := logger_with(j)
	for _, message := range []string{"connection probe failed", "probe target measurement failed"} {
		logger.Warn(message, "error", "timeout")
		if !strings.Contains(out.String(), message) {
			t.Errorf("file log lost diagnostic %q", message)
		}
	}
	logger.Info("event", "kind", "connection_lost")
	logger.Warn("retry on the same node failed to connect", "error", "timeout")
	logger.Warn("unrecognized operational warning")
	got := j.List()
	if len(got) != 3 {
		t.Fatalf("kept %d entries, want 3: %+v", len(got), got)
	}
	if got[0].Message != "event" || got[0].Attrs["kind"] != "connection_lost" {
		t.Errorf("connection loss was not retained: %+v", got[0])
	}
	if got[1].Message != "retry on the same node failed to connect" || got[2].Message != "unrecognized operational warning" {
		t.Errorf("operational warnings were not retained: %+v", got)
	}
}

func Test_the_ring_keeps_the_newest_oldest_first(t *testing.T) {
	j := New(3)
	logger, _ := logger_with(j)
	for i := range 5 {
		logger.Info("connect requested", "n", i)
	}
	got := j.List()
	if len(got) != 3 {
		t.Fatalf("kept %d, want 3", len(got))
	}
	for i, e := range got {
		if e.Attrs["n"] != strconv.Itoa(i+2) {
			t.Errorf("entry %d is n=%s, want %d", i, e.Attrs["n"], i+2)
		}
	}
}

// A message renamed at its call site would drop out of the journal with no
// error anywhere; every operation must still be written somewhere in the
// code.
func Test_every_operation_is_still_logged_somewhere(t *testing.T) {
	var sources strings.Builder
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (d.Name() == "journal" || strings.HasPrefix(d.Name(), ".") || d.Name() == "graft" || d.Name() == "local" || d.Name() == "dist") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sources.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for message := range Operations {
		if !strings.Contains(sources.String(), strconv.Quote(message)) {
			t.Errorf("no code logs %q any more; the journal would never show it", message)
		}
	}
}

func Test_the_journal_outlives_the_service_and_marks_a_run_that_never_stopped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	first := Open(path, 10)
	logger := slog.New(Handler(slog.NewTextHandler(io.Discard, nil), first))
	logger.Info("service starting")
	logger.Info(Stopped)
	if err := first.Flush(); err != nil {
		t.Fatal(err)
	}

	// A clean stop: the next run reads it back as it was.
	second := Open(path, 10)
	if got := second.List(); len(got) != 2 || got[1].Message != Stopped {
		t.Fatalf("after a clean stop: %+v", got)
	}
	slog.New(Handler(slog.NewTextHandler(io.Discard, nil), second)).Info("service starting")
	if err := second.Flush(); err != nil {
		t.Fatal(err)
	}

	// The power went: the run after it says so, at the last time heard.
	third := Open(path, 10)
	got := third.List()
	last := got[len(got)-1]
	if last.Message != Ended_unstopped || last.Level != "warn" || !last.At.Equal(got[len(got)-2].At) {
		t.Fatalf("after a run that never stopped: %+v", got)
	}
}
