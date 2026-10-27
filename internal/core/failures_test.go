package core

import (
	"testing"
	"time"

	"singularity-vpn-app/internal/journal"
)

var failure_now = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// ago places an entry the given time before failure_now.
func ago(d time.Duration, message string, kind string) journal.Entry {
	entry := journal.Entry{At: failure_now.Add(-d), Level: "info", Message: message}
	if kind != "" {
		entry.Attrs = map[string]string{"kind": kind}
	}
	return entry
}

func blip(d time.Duration) journal.Entry { return ago(d, "event", "connection_blip") }
func lost(d time.Duration) journal.Entry { return ago(d, "event", "connection_lost") }

func Test_failure_signal(t *testing.T) {
	cases := []struct {
		name    string
		entries []journal.Entry
		// count, blips, lost, failed are -1 for "no signal".
		count, blips, lost, failed int
	}{
		{"nothing at all", nil, -1, 0, 0, 0},
		{"three blips are enough", []journal.Entry{blip(20 * time.Minute), blip(10 * time.Minute), blip(time.Minute)}, 3, 3, 0, 0},
		{"two blips are not", []journal.Entry{blip(10 * time.Minute), blip(time.Minute)}, -1, 0, 0, 0},
		{"a lost and a blip", []journal.Entry{lost(10 * time.Minute), blip(time.Minute)}, 2, 1, 1, 0},
		{"a lost alone is not", []journal.Entry{lost(time.Minute)}, -1, 0, 0, 0},
		{"a failed connect and a blip", []journal.Entry{ago(5*time.Minute, "verification failed", ""), blip(time.Minute)}, 2, 1, 0, 1},
		{"a switch that failed counts as lost", []journal.Entry{ago(5*time.Minute, "event", "switch_failed"), blip(time.Minute)}, 2, 1, 1, 0},
		{"the service stopping with an error", []journal.Entry{ago(5*time.Minute, journal.Stopped_with_error, ""), blip(time.Minute)}, 2, 1, 0, 1},
		{"both retries failing", []journal.Entry{ago(5*time.Minute, "retry on the same node failed to connect", ""), ago(4*time.Minute, "retry on the same node failed verification", "")}, 2, 0, 0, 2},
		{
			"a network change explains what follows within a minute",
			[]journal.Entry{ago(10*time.Minute, "event", "network_changed"), lost(10*time.Minute - 30*time.Second), blip(10*time.Minute - 59*time.Second)},
			-1, 0, 0, 0,
		},
		{
			"a network change explains nothing after a minute",
			[]journal.Entry{ago(10*time.Minute, "event", "network_changed"), lost(10*time.Minute - 61*time.Second), blip(5 * time.Minute)},
			2, 1, 1, 0,
		},
		{
			// The entry is journaled after the change has settled, a few
			// seconds on, so the failures it caused come before it.
			"a network change explains what came within ten seconds before it",
			[]journal.Entry{lost(10*time.Minute + 9*time.Second), blip(10*time.Minute + 10*time.Second), ago(10*time.Minute, "event", "network_changed")},
			-1, 0, 0, 0,
		},
		{
			"a network change explains nothing longer before it",
			[]journal.Entry{lost(10*time.Minute + 11*time.Second), blip(10*time.Minute + 12*time.Second), ago(10*time.Minute, "event", "network_changed")},
			2, 1, 1, 0,
		},
		{
			"a network change only explains the failures near it",
			[]journal.Entry{lost(10*time.Minute + 5*time.Second), ago(10*time.Minute, "event", "network_changed"), lost(10*time.Minute - 30*time.Second), lost(2 * time.Minute), blip(time.Minute)},
			2, 1, 1, 0,
		},
		{"entries older than thirty minutes are history", []journal.Entry{lost(31 * time.Minute), blip(40 * time.Minute), blip(2 * time.Hour)}, -1, 0, 0, 0},
		{"only the old ones are left out", []journal.Entry{blip(31 * time.Minute), lost(29 * time.Minute), blip(time.Minute)}, 2, 1, 1, 0},
		{
			"failures after a disconnect are the person's doing",
			[]journal.Entry{ago(10*time.Minute, "disconnect requested", ""), lost(9 * time.Minute), blip(8 * time.Minute), blip(7 * time.Minute)},
			-1, 0, 0, 0,
		},
		{
			"failures count again after the next connect",
			[]journal.Entry{ago(10*time.Minute, "disconnect requested", ""), lost(9 * time.Minute), ago(8*time.Minute, "connect requested", ""), lost(7 * time.Minute), blip(6 * time.Minute)},
			2, 1, 1, 0,
		},
		{
			"a disconnect before the window still holds in it",
			[]journal.Entry{ago(2*time.Hour, "disconnect requested", ""), lost(10 * time.Minute), blip(9 * time.Minute)},
			-1, 0, 0, 0,
		},
		{
			"failures before a disconnect still count",
			[]journal.Entry{lost(10 * time.Minute), blip(9 * time.Minute), ago(8*time.Minute, "disconnect requested", "")},
			2, 1, 1, 0,
		},
		{"events that are not failures are ignored", []journal.Entry{ago(5*time.Minute, "event", "connection_established"), ago(4*time.Minute, "event", "connection_degraded"), ago(3*time.Minute, "event", "switch_started"), ago(2*time.Minute, "strategy set", "")}, -1, 0, 0, 0},
		{"a message that merely looks like an event kind is not one", []journal.Entry{ago(3*time.Minute, "connection_lost", ""), ago(2*time.Minute, "connection_blip", ""), ago(time.Minute, "connection_blip", "")}, -1, 0, 0, 0},
		{"an entry from the future is not counted", []journal.Entry{blip(-time.Minute), blip(-2 * time.Minute), blip(-3 * time.Minute)}, -1, 0, 0, 0},
		{"entries out of order are read in time order", []journal.Entry{blip(time.Minute), ago(10*time.Minute, "disconnect requested", ""), lost(9 * time.Minute), ago(8*time.Minute, "connect requested", ""), lost(7 * time.Minute)}, 2, 1, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := failure_signal(tc.entries, failure_now)
			if tc.count < 0 {
				if got != nil {
					t.Fatalf("signal = %+v, want none", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("no signal, want %d failures", tc.count)
			}
			if got.Count != tc.count || got.Blips != tc.blips || got.Lost != tc.lost || got.Failed != tc.failed {
				t.Fatalf("signal = %+v, want count=%d blips=%d lost=%d failed=%d", *got, tc.count, tc.blips, tc.lost, tc.failed)
			}
		})
	}
}

func Test_failure_signal_names_the_first_and_last_failure_counted(t *testing.T) {
	entries := []journal.Entry{
		blip(31 * time.Minute), // too old to count, so not the first
		lost(20 * time.Minute),
		blip(10 * time.Minute),
		ago(5*time.Minute, "strategy set", ""),
	}
	got := failure_signal(entries, failure_now)
	if got == nil {
		t.Fatal("no signal")
	}
	if !got.First.Equal(failure_now.Add(-20*time.Minute)) || !got.Last.Equal(failure_now.Add(-10*time.Minute)) {
		t.Fatalf("first = %v, last = %v", got.First, got.Last)
	}
}

func Test_failure_signal_leaves_the_entries_it_was_given_alone(t *testing.T) {
	entries := []journal.Entry{blip(time.Minute), blip(2 * time.Minute), blip(3 * time.Minute)}
	failure_signal(entries, failure_now)
	if !entries[0].At.Equal(failure_now.Add(-time.Minute)) {
		t.Fatal("the journal's own order was changed")
	}
}

// The journal keeps only the messages it lists; a failure message renamed in
// the code would silently stop being seen here, so each is held to that list.
func Test_every_failure_the_signal_reads_is_one_the_journal_keeps(t *testing.T) {
	for message := range failure_messages {
		if message == journal.Stopped_with_error {
			continue // an error-level record, kept whatever its message
		}
		if !journal.Operations[message] {
			t.Errorf("the journal does not keep %q", message)
		}
	}
	for _, kind := range []string{"connection_blip", "connection_lost", "switch_failed", "network_changed"} {
		if !journal.Events[kind] {
			t.Errorf("the journal does not keep the %q event", kind)
		}
	}
	for _, message := range []string{"connect requested", "disconnect requested"} {
		if !journal.Operations[message] {
			t.Errorf("the journal does not keep %q", message)
		}
	}
	for kind := range failure_events {
		if !journal.Events[kind] {
			t.Errorf("the journal does not keep the %q event", kind)
		}
	}
}
