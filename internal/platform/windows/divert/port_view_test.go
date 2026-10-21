package divert

import (
	"testing"
	"time"
)

// fake_clock is a judge whose sleeps move its own clock, so a test sees
// the time a hold would really take.
type fake_clock struct {
	at     time.Time
	sleeps int
	// on_sleep, when set, runs after each sleep.
	on_sleep func()
}

func judge_on(clock *fake_clock, load func() *port_view) *port_judge {
	j := new_port_judge(load)
	j.now = func() time.Time { return clock.at }
	j.sleep = func(d time.Duration) {
		clock.at = clock.at.Add(d)
		clock.sleeps++
		if clock.on_sleep != nil {
			clock.on_sleep()
		}
	}
	return j
}

func new_clock() *fake_clock { return &fake_clock{at: time.Unix(1000, 0)} }

func Test_a_known_port_is_judged_without_waiting(t *testing.T) {
	view := new_port_view([]Port{{Udp, 7086}}, []Port{{Udp, 7086}, {Tcp, 443}})
	clock := new_clock()
	j := judge_on(clock, func() *port_view { return view })
	if !j.is_game(Port{Udp, 7086}, true) {
		t.Error("a game's port was not judged a game's")
	}
	if j.is_game(Port{Tcp, 443}, true) {
		t.Error("another program's port was judged a game's")
	}
	if clock.sleeps != 0 {
		t.Errorf("waited %d times for known ports", clock.sleeps)
	}
}

// The first packet of a game socket can beat its bind event to the table.
// It is held until the table hears of it, then carried like the rest.
func Test_a_port_the_table_learns_while_held_is_judged_by_it(t *testing.T) {
	current := new_port_view(nil, nil)
	clock := new_clock()
	clock.on_sleep = func() {
		if clock.sleeps == 3 {
			current = new_port_view([]Port{{Udp, 7086}}, []Port{{Udp, 7086}})
		}
	}
	j := judge_on(clock, func() *port_view { return current })
	if !j.is_game(Port{Udp, 7086}, true) {
		t.Fatal("a game port learned while held was not judged a game's")
	}
	if clock.sleeps != 3 {
		t.Errorf("slept %d times, want 3", clock.sleeps)
	}
}

func Test_a_hold_lasts_unknown_wait_by_the_clock(t *testing.T) {
	view := new_port_view(nil, nil)
	clock := new_clock()
	j := judge_on(clock, func() *port_view { return view })
	// A coarse timer: each 1 ms sleep really takes 5 ms.
	j.sleep = func(time.Duration) { clock.at = clock.at.Add(5 * time.Millisecond); clock.sleeps++ }
	start := clock.at
	if j.is_game(Port{Udp, 5000}, true) {
		t.Fatal("an unknown port was judged a game's")
	}
	if spent := clock.at.Sub(start); spent < unknown_wait || spent > unknown_wait+5*time.Millisecond {
		t.Errorf("held %v, want about %v", spent, unknown_wait)
	}
}

func Test_a_port_never_learned_costs_one_wait(t *testing.T) {
	view := new_port_view(nil, nil)
	clock := new_clock()
	j := judge_on(clock, func() *port_view { return view })
	j.is_game(Port{Udp, 5000}, true)
	first := clock.sleeps
	if j.is_game(Port{Udp, 5000}, true) {
		t.Fatal("an unknown port was judged a game's")
	}
	if clock.sleeps != first {
		t.Errorf("waited again for a port already waited for")
	}
}

// A socket the table has dropped on close still sends its FIN or RST; the
// verdict it had stands, with no wait.
func Test_a_closed_port_keeps_its_verdict_without_waiting(t *testing.T) {
	current := new_port_view([]Port{{Tcp, 50000}}, []Port{{Tcp, 50000}})
	clock := new_clock()
	j := judge_on(clock, func() *port_view { return current })
	if !j.is_game(Port{Tcp, 50000}, true) {
		t.Fatal("a game port was not judged a game's")
	}
	current = new_port_view(nil, nil)
	if !j.is_game(Port{Tcp, 50000}, true) {
		t.Error("a game socket's last packets lost their verdict after close")
	}
	if clock.sleeps != 0 {
		t.Errorf("waited %d times for a port already decided", clock.sleeps)
	}
}

func Test_a_packet_that_cannot_open_a_flow_is_not_held(t *testing.T) {
	view := new_port_view(nil, nil)
	clock := new_clock()
	j := judge_on(clock, func() *port_view { return view })
	if j.is_game(Port{Tcp, 5000}, false) {
		t.Fatal("an unknown port was judged a game's")
	}
	if clock.sleeps != 0 {
		t.Errorf("held a mid-flow packet %d times", clock.sleeps)
	}
}

// Many unknown ports at once -- a browser closing its connections, a scan
// -- may not stall the machine: holding stops at the budget for the second.
func Test_holding_stops_at_the_budget(t *testing.T) {
	view := new_port_view(nil, nil)
	clock := new_clock()
	j := judge_on(clock, func() *port_view { return view })
	start := clock.at
	for port := uint16(1); port <= 50; port++ {
		j.is_game(Port{Udp, port}, true)
	}
	if spent := clock.at.Sub(start); spent > hold_budget+unknown_step {
		t.Errorf("50 unknown ports held %v in total, budget %v", spent, hold_budget)
	}
	// The next second has its budget again.
	clock.at = start.Add(time.Second)
	before := clock.sleeps
	j.is_game(Port{Udp, 60000}, true)
	if clock.sleeps == before {
		t.Error("no hold in the next second")
	}
}

func Test_verdicts_are_forgotten_after_a_refresh(t *testing.T) {
	view := new_port_view(nil, nil)
	clock := new_clock()
	j := judge_on(clock, func() *port_view { return view })
	j.is_game(Port{Udp, 5000}, true)
	first := clock.sleeps
	clock.at = clock.at.Add(forget_every + time.Second)
	j.is_game(Port{Udp, 5000}, true)
	if clock.sleeps == first {
		t.Error("did not wait again after a refresh")
	}
}

func Test_no_view_yet_is_not_a_game(t *testing.T) {
	clock := new_clock()
	j := judge_on(clock, func() *port_view { return nil })
	if j.is_game(Port{Udp, 7086}, true) {
		t.Fatal("judged a game's before any view was published")
	}
}
