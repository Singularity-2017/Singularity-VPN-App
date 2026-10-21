package core

import (
	"testing"
	"time"
)

func Test_a_match_starts_at_the_first_stream_and_ends_after_a_quiet_stretch(t *testing.T) {
	var w match_watch
	at := time.Date(2026, 10, 21, 6, 0, 0, 0, time.UTC)
	steps := []struct {
		after     time.Duration
		streaming bool
		on        bool
		changed   bool
	}{
		{0, false, false, false},
		{5 * time.Second, true, true, true},
		{5 * time.Second, false, true, false}, // a loading screen
		{20 * time.Second, false, true, false},
		{5 * time.Second, true, true, false},
		{25 * time.Second, false, true, false},
		{5 * time.Second, false, false, true}, // 30 s since the last stream
		{5 * time.Second, false, false, false},
	}
	for i, s := range steps {
		at = at.Add(s.after)
		on, changed := w.observe(at, s.streaming)
		if on != s.on || changed != s.changed {
			t.Fatalf("step %d: observe = %v %v, want %v %v", i, on, changed, s.on, s.changed)
		}
	}
}
