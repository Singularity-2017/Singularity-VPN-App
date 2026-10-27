//go:build windows

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var foreground_start = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return foreground_start.Add(time.Duration(seconds) * time.Second) }

func Test_consecutive_samples_of_one_program_are_one_run(t *testing.T) {
	tracker := new_foreground_tracker()
	tracker.note("cs2.exe", at(0))
	tracker.note("cs2.exe", at(5))
	tracker.note("cs2.exe", at(10))
	runs := tracker.snapshot(at(10))
	if len(runs) != 1 || runs[0].Exe != "cs2.exe" || !runs[0].From.Equal(at(0)) || !runs[0].To.Equal(at(10)) {
		t.Fatalf("runs = %+v", runs)
	}
}

func Test_another_program_starts_another_run_and_the_first_can_return(t *testing.T) {
	tracker := new_foreground_tracker()
	tracker.note("cs2.exe", at(0))
	tracker.note("cs2.exe", at(5))
	tracker.note("chrome.exe", at(10))
	tracker.note("cs2.exe", at(15))
	runs := tracker.snapshot(at(15))
	if len(runs) != 3 || runs[0].Exe != "cs2.exe" || runs[1].Exe != "chrome.exe" || runs[2].Exe != "cs2.exe" {
		t.Fatalf("runs = %+v", runs)
	}
	if !runs[0].To.Equal(at(5)) || !runs[1].From.Equal(at(10)) || !runs[2].From.Equal(at(15)) {
		t.Fatalf("runs = %+v", runs)
	}
}

func Test_a_sample_with_nothing_in_front_changes_nothing(t *testing.T) {
	tracker := new_foreground_tracker()
	tracker.note("", at(0))
	if runs := tracker.snapshot(at(0)); len(runs) != 0 {
		t.Fatalf("runs = %+v", runs)
	}
	tracker.note("cs2.exe", at(5))
	tracker.note("", at(10)) // a protected window in front for a moment
	tracker.note("cs2.exe", at(15))
	if runs := tracker.snapshot(at(15)); len(runs) != 1 || !runs[0].To.Equal(at(15)) {
		t.Fatalf("a short gap split the run: %+v", runs)
	}
}

func Test_a_long_absence_ends_a_run(t *testing.T) {
	tracker := new_foreground_tracker()
	tracker.note("cs2.exe", at(0))
	tracker.note("cs2.exe", at(int(foreground_gap/time.Second)+1))
	if runs := tracker.snapshot(at(60)); len(runs) != 2 {
		t.Fatalf("a program unseen for longer than the gap is one run: %+v", runs)
	}
}

func Test_runs_older_than_two_hours_are_forgotten(t *testing.T) {
	tracker := new_foreground_tracker()
	tracker.note("old.exe", at(0))
	tracker.note("new.exe", at(3600))
	if runs := tracker.snapshot(at(7200)); len(runs) != 2 {
		t.Fatalf("a run that ended exactly two hours ago was dropped: %+v", runs)
	}
	runs := tracker.snapshot(at(7201))
	if len(runs) != 1 || runs[0].Exe != "new.exe" {
		t.Fatalf("runs = %+v", runs)
	}
	// Taking a snapshot long after forgets what has aged out.
	if runs := tracker.snapshot(at(3600 + 3*3600)); len(runs) != 0 {
		t.Fatalf("runs = %+v", runs)
	}
}

func Test_no_more_than_two_hundred_runs_are_kept_and_the_oldest_go(t *testing.T) {
	tracker := new_foreground_tracker()
	for i := 0; i < 300; i++ {
		tracker.note(fmt.Sprintf("app%03d.exe", i), at(i*10))
	}
	runs := tracker.snapshot(at(299 * 10))
	if len(runs) != foreground_max_runs {
		t.Fatalf("%d runs kept", len(runs))
	}
	if runs[0].Exe != "app100.exe" || runs[len(runs)-1].Exe != "app299.exe" {
		t.Fatalf("the newest were not the ones kept: first %s, last %s", runs[0].Exe, runs[len(runs)-1].Exe)
	}
}

func Test_a_snapshot_is_a_copy(t *testing.T) {
	tracker := new_foreground_tracker()
	tracker.note("cs2.exe", at(0))
	runs := tracker.snapshot(at(0))
	runs[0].Exe = "changed.exe"
	tracker.note("cs2.exe", at(5))
	if got := tracker.snapshot(at(5)); got[0].Exe != "cs2.exe" {
		t.Fatalf("the tracker followed a change made to a snapshot: %+v", got)
	}
}

func Test_only_the_base_name_is_kept(t *testing.T) {
	cases := map[string]string{
		`C:\Program Files\Steam\steamapps\common\cs2\cs2.exe`: "cs2.exe",
		`D:/games/cs2.exe`:                    "cs2.exe",
		`cs2.exe`:                             "cs2.exe",
		`\?\C:\Users\someone\AppData\app.exe`: "app.exe",
		``:                                    "",
	}
	for path, want := range cases {
		if got := base_name(path); got != want {
			t.Errorf("base_name(%q) = %q, want %q", path, got, want)
		}
	}
}

// The foreground program of the machine running the tests can be anything,
// or nothing; what it must be is a name, not a path.
func Test_the_foreground_program_is_a_bare_name(t *testing.T) {
	if name := foreground_exe(); strings.ContainsAny(name, `\/`) {
		t.Fatalf("foreground_exe() = %q carries a path", name)
	}
}
