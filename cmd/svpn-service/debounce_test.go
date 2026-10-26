package main

import (
	"testing"
	"time"
)

// fake_clock is a scheduler driven by hand: advance moves time and fires
// what has come due, in order.
type fake_clock struct {
	now     time.Duration
	pending []*fake_timer
}

type fake_timer struct {
	at        time.Duration
	f         func()
	cancelled bool
}

func (c *fake_clock) schedule(d time.Duration, f func()) func() {
	timer := &fake_timer{at: c.now + d, f: f}
	c.pending = append(c.pending, timer)
	return func() { timer.cancelled = true }
}

func (c *fake_clock) advance(d time.Duration) {
	c.now += d
	for _, timer := range c.pending {
		if !timer.cancelled && timer.at <= c.now {
			timer.cancelled = true
			timer.f()
		}
	}
}

func Test_a_burst_of_changes_calls_once_after_the_last(t *testing.T) {
	clock := &fake_clock{}
	calls := 0
	d := new_debouncer(3*time.Second, clock.schedule, func() { calls++ })

	d.Trigger()
	clock.advance(2 * time.Second)
	d.Trigger()
	clock.advance(2 * time.Second)
	d.Trigger()
	clock.advance(2 * time.Second)
	if calls != 0 {
		t.Fatalf("calls = %d before the burst settled, want 0", calls)
	}
	clock.advance(2 * time.Second)
	if calls != 1 {
		t.Fatalf("calls = %d after the burst, want 1", calls)
	}
}

func Test_two_bursts_far_apart_call_twice(t *testing.T) {
	clock := &fake_clock{}
	calls := 0
	d := new_debouncer(3*time.Second, clock.schedule, func() { calls++ })

	d.Trigger()
	clock.advance(4 * time.Second)
	clock.advance(time.Minute)
	d.Trigger()
	d.Trigger()
	clock.advance(4 * time.Second)
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func Test_stop_prevents_the_pending_call_and_later_triggers(t *testing.T) {
	clock := &fake_clock{}
	calls := 0
	d := new_debouncer(3*time.Second, clock.schedule, func() { calls++ })

	d.Trigger()
	d.Stop()
	clock.advance(10 * time.Second)
	d.Trigger()
	clock.advance(10 * time.Second)
	if calls != 0 {
		t.Fatalf("calls = %d after Stop, want 0", calls)
	}
}

func Test_network_moved_filters_only_virtual_churn_on_a_known_path(t *testing.T) {
	cases := []struct {
		name          string
		touched       bool
		before, after string
		want          bool
	}{
		{"virtual churn, same path", false, "a", "a", false},
		{"hardware adapter notified, same path", true, "a", "a", true},
		{"path moved", false, "a", "b", true},
		{"path appeared", false, "", "a", true},
		{"path went away", false, "a", "", true},
		{"no physical path at all", false, "", "", true},
	}
	for _, c := range cases {
		if got := network_moved(c.touched, c.before, c.after); got != c.want {
			t.Errorf("%s: network_moved = %v, want %v", c.name, got, c.want)
		}
	}
}

func Test_virtual_churn_does_not_read_the_routing_tables(t *testing.T) {
	cases := []struct {
		touched bool
		before  string
		want    bool
	}{
		{false, "eth|net|gw|src", false}, // Hyper-V, WSL, Docker rebuilding routes
		{true, "eth|net|gw|src", true},   // the physical adapter itself changed
		{false, "", true},                // no physical path known: nothing to compare against
	}
	for _, c := range cases {
		if got := path_may_have_moved(c.touched, c.before); got != c.want {
			t.Fatalf("path_may_have_moved(%v, %q) = %v, want %v", c.touched, c.before, got, c.want)
		}
	}
}
