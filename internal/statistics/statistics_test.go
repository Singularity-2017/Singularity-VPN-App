package statistics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func Test_duration_checkpoints_and_restart_exclude_downtime(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "statistics.json")
	s, err := Open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	transition := func(from, to string, elapsed time.Duration) {
		now = now.Add(elapsed)
		s.Transition(from, to, now)
	}
	transition("disconnected", "connecting", 0)
	transition("connecting", "connected", 5*time.Second)
	transition("connected", "degraded", 10*time.Second)
	transition("degraded", "connected", 10*time.Second)
	now = now.Add(10 * time.Second)
	for i := 0; i < 2; i++ {
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}
	transition("connected", "switching", 0)
	s.Lost()
	transition("switching", "connected", 20*time.Second)
	s.Switched()
	transition("connected", "disconnecting", 5*time.Second)
	transition("disconnecting", "disconnected", 3*time.Second)
	got := s.Snapshot()
	if got.Total_connected_seconds != 35 || got.Connections != 1 || got.Reconnections != 1 || got.Losses != 1 || got.Switches != 1 || got.Disconnects != 1 {
		t.Fatalf("wrong totals: %+v", got)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	restored, err := Open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Snapshot(); got.Total_connected_seconds != 35 || got.Connections != 1 {
		t.Fatalf("restart counted offline time: %+v", got)
	}
}

func Test_observations_are_deduplicated_and_unknown_averages_are_absent(t *testing.T) {
	now := time.Now()
	s, err := Open(filepath.Join(t.TempDir(), "stats.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Average_rtt_ms != nil {
		t.Fatal("unknown RTT was invented")
	}
	s.Observe_sample(now, 50*time.Millisecond, .25)
	s.Observe_sample(now, 100*time.Millisecond, .5)
	s.Observe_sample(now.Add(time.Second), time.Millisecond, 0)
	now = now.Add(time.Second)
	s.Observe_sample(now, 150*time.Millisecond, 0)
	got := s.Snapshot()
	if got.Probe_samples != 2 || *got.Average_rtt_ms != 100 || *got.Average_loss_percent != 12.5 {
		t.Fatalf("wrong averages: %+v", got)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(s.path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if *restored.Snapshot().Average_rtt_ms != 100 {
		t.Fatal("average not persisted")
	}
}

func Test_corrupt_record_is_not_overwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	const bad = "{\"version\":1,"
	if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Fatal("accepted corrupt history")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != bad {
		t.Fatal("corrupt history was changed")
	}
}

func Test_finish_and_failures_do_not_invent_connected_time(t *testing.T) {
	now := time.Now()
	s, _ := Open(filepath.Join(t.TempDir(), "stats.json"), func() time.Time { return now })
	s.Transition("disconnected", "connecting", now)
	s.Transition("connecting", "failed", now)
	s.Transition("failed", "connecting", now)
	s.Transition("connecting", "connected", now)
	now = now.Add(time.Minute)
	s.Finish()
	now = now.Add(time.Hour)
	s.Finish()
	got := s.Snapshot()
	if got.Total_connected_seconds != 60 || got.Failed_connections != 1 || got.Disconnects != 0 {
		t.Fatalf("wrong final totals: %+v", got)
	}
}

func Test_udp_loss_counts_every_burst_and_rtt_only_the_answered(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	s, err := Open(filepath.Join(t.TempDir(), "statistics.json"), func() time.Time { return now.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	s.Observe_udp(now, 50*time.Millisecond, 0)
	s.Observe_udp(now, 70*time.Millisecond, .5) // the same burst again
	s.Observe_udp(now.Add(time.Second), 0, 1)   // nothing came back
	snapshot := s.Snapshot()
	if snapshot.Udp_samples != 2 || snapshot.Average_udp_loss_percent == nil || *snapshot.Average_udp_loss_percent != 50 ||
		snapshot.Average_udp_rtt_ms == nil || *snapshot.Average_udp_rtt_ms != 50 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
