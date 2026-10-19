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
