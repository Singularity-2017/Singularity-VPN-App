//go:build windows

package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"singularity-vpn-app/internal/ipc"
)

// The window process sees the desktop and the service does not, so a problem
// report asks the window which programs were in front. That is what lets a
// person who says "the game drops" be matched with the game. It is kept in
// memory only, as base names only, for two hours at most: never written to
// disk, never logged, and sent only inside a problem report the person was
// shown before it was sent.
const (
	// foreground_interval is how often the foreground program is looked at.
	foreground_interval = 5 * time.Second
	// foreground_keep is how far back the runs reach.
	foreground_keep = 2 * time.Hour
	// foreground_max_runs is how many runs are kept; ipc.Problem_draft_params
	// accepts no more.
	foreground_max_runs = 200
	// foreground_gap is how long a program may go unseen -- a locked screen,
	// a protected window -- and still be the same run when it is seen again.
	foreground_gap = 3 * foreground_interval
)

// foreground_tracker keeps the runs of programs that were in the foreground:
// consecutive samples of one program are one run.
type foreground_tracker struct {
	mu   sync.Mutex
	runs []ipc.App_use
}

func new_foreground_tracker() *foreground_tracker { return &foreground_tracker{} }

// note records that exe was in the foreground at moment at. An empty name is a
// sample with nothing to record -- no window, or one that could not be read --
// and leaves the runs as they were.
func (t *foreground_tracker) note(exe string, at time.Time) {
	if exe == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.runs); n > 0 && t.runs[n-1].Exe == exe && at.Sub(t.runs[n-1].To) <= foreground_gap {
		t.runs[n-1].To = at
	} else {
		t.runs = append(t.runs, ipc.App_use{Exe: exe, From: at, To: at})
	}
	t.trim(at)
}

// trim drops the runs that ended before the keep window and the oldest beyond
// the cap; the caller holds mu.
func (t *foreground_tracker) trim(now time.Time) {
	cutoff := now.Add(-foreground_keep)
	first := 0
	for first < len(t.runs) && t.runs[first].To.Before(cutoff) {
		first++
	}
	if over := len(t.runs) - first - foreground_max_runs; over > 0 {
		first += over
	}
	if first > 0 {
		t.runs = append([]ipc.App_use(nil), t.runs[first:]...)
	}
}

// snapshot is the runs as they stand at now, a copy the caller may keep.
func (t *foreground_tracker) snapshot(now time.Time) []ipc.App_use {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.trim(now)
	return append([]ipc.App_use(nil), t.runs...)
}

// run looks at the foreground every foreground_interval until ctx ends.
func (t *foreground_tracker) run(ctx context.Context, sample func() string) {
	ticker := time.NewTicker(foreground_interval)
	defer ticker.Stop()
	for {
		t.note(sample(), time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// base_name is the last element of a Windows path, whichever slash it uses.
// Only this ever leaves foreground_windows.go: a full path names the folders
// of somebody's profile.
func base_name(path string) string {
	return path[strings.LastIndexAny(path, `\/`)+1:]
}
