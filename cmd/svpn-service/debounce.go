package main

import (
	"sync"
	"time"
)

// scheduler runs f after d and returns a function that cancels it. It is
// time.AfterFunc in production and a hand-driven fake in tests.
type scheduler func(d time.Duration, f func()) (cancel func())

func real_scheduler(d time.Duration, f func()) func() {
	timer := time.AfterFunc(d, f)
	return func() { timer.Stop() }
}

// debouncer turns a burst of Trigger calls into one call of fn, delay after
// the last of the burst. Network changes arrive as dozens of notifications
// for one real event, and the reaction (a route move, a probe) is far more
// expensive than the notification.
type debouncer struct {
	delay    time.Duration
	schedule scheduler
	fn       func()

	mu      sync.Mutex
	cancel  func()
	gen     uint64
	stopped bool
}

func new_debouncer(delay time.Duration, schedule scheduler, fn func()) *debouncer {
	return &debouncer{delay: delay, schedule: schedule, fn: fn}
}

// Trigger records a change and (re)starts the wait. It never blocks on fn.
func (d *debouncer) Trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.gen++
	gen := d.gen
	d.cancel = d.schedule(d.delay, func() { d.fire(gen) })
}

// fire runs fn unless a newer Trigger or a Stop has overtaken this timer.
func (d *debouncer) fire(gen uint64) {
	d.mu.Lock()
	if d.stopped || gen != d.gen {
		d.mu.Unlock()
		return
	}
	d.cancel = nil
	d.mu.Unlock()
	d.fn()
}

// Stop cancels a pending call and refuses further ones.
func (d *debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
}

// network_moved decides whether a settled burst of route and interface
// notifications is a network change. A notification from a hardware adapter
// always is: a cable pulled and put back can keep every address and still
// have dropped the routes this program pinned. Churn on virtual adapters --
// another VPN, Hyper-V, WSL rebuilding their own routes -- is one only when
// it moved the physical path. Without a physical path to compare (a machine
// whose default route runs through a bridge or a virtual switch) nothing can
// be told apart, and every burst counts, as it did before the filter.
func network_moved(hardware_touched bool, path_before, path_after string) bool {
	return hardware_touched || path_before != path_after || path_after == ""
}

// path_may_have_moved says whether a settled burst of notifications is worth
// reading the routing tables for. Every row a physical path is made of --
// its default route, its address, its interface -- belongs to a hardware
// adapter, so churn on virtual ones alone cannot move a path that exists.
// With no physical path, there is nothing to compare against, and the
// tables are read.
func path_may_have_moved(hardware_touched bool, path_before string) bool {
	return hardware_touched || path_before == ""
}
