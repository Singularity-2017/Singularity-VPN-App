//go:build windows

package main

import "testing"

func Test_widening_keeps_the_window_inside_the_work_area(t *testing.T) {
	work := win_rect{left: 0, top: 0, right: 1920, bottom: 1040}
	cases := []struct {
		name        string
		window      win_rect
		width       int32
		left, right int32
	}{
		{"room on the right", win_rect{left: 100, right: 540}, 800, 100, 900},
		{"at the right edge", win_rect{left: 1400, right: 1840}, 800, 1120, 1920},
		{"already past the edge", win_rect{left: 1700, right: 2140}, 800, 1120, 1920},
		{"wider than the screen", win_rect{left: 100, right: 540}, 2400, 0, 1920},
		{"narrowing stays put", win_rect{left: 1120, right: 1920}, 440, 1120, 1560},
	}
	for _, c := range cases {
		left, right := fit_width(c.window, work, c.width)
		if left != c.left || right != c.right {
			t.Errorf("%s: got %d..%d, want %d..%d", c.name, left, right, c.left, c.right)
		}
	}
}

func Test_a_second_monitor_keeps_its_own_offset(t *testing.T) {
	work := win_rect{left: -1920, top: 0, right: 0, bottom: 1040}
	left, right := fit_width(win_rect{left: -500, right: -60}, work, 800)
	if left != -800 || right != 0 {
		t.Fatalf("got %d..%d, want -800..0", left, right)
	}
}

func Test_a_window_at_the_right_edge_stays_there_and_one_put_back_returns(t *testing.T) {
	area := win_rect{left: -7, top: 0, right: 1927, bottom: 1047}
	// Docked right: widening and narrowing both keep the right edge.
	if left, right := place_width(win_rect{left: 1487, right: 1927}, area, 800, nil); left != 1127 || right != 1927 {
		t.Errorf("widening a docked window: got %d..%d, want 1127..1927", left, right)
	}
	from := int32(1300)
	if left, right := place_width(win_rect{left: 1127, right: 1927}, area, 440, &from); left != 1487 || right != 1927 {
		t.Errorf("narrowing a docked window: got %d..%d, want 1487..1927", left, right)
	}
	// Not docked: narrowing goes back to where widening started.
	if left, right := place_width(win_rect{left: 1100, right: 1900}, area, 440, &from); left != 1300 || right != 1740 {
		t.Errorf("narrowing back: got %d..%d, want 1300..1740", left, right)
	}
}

func Test_a_dragged_window_snaps_onto_a_near_edge_and_not_a_far_one(t *testing.T) {
	area := win_rect{left: -7, top: 0, right: 1927, bottom: 1047}
	cases := []struct {
		name         string
		window, want win_rect
	}{
		{"near the left", win_rect{3, 200, 443, 1000}, win_rect{-7, 200, 433, 1000}},
		{"near the right and the top", win_rect{1480, 10, 1920, 810}, win_rect{1487, 0, 1927, 800}},
		{"near the bottom", win_rect{600, 250, 1040, 1050}, win_rect{600, 247, 1040, 1047}},
		{"in the middle", win_rect{600, 100, 1040, 900}, win_rect{600, 100, 1040, 900}},
	}
	for _, c := range cases {
		if got := stick(c.window, area, 16); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func Test_a_snapped_window_comes_off_once_the_pointer_has_gone_past_the_distance(t *testing.T) {
	area := win_rect{left: -7, top: 0, right: 1927, bottom: 1047}
	start := win_rect{-7, 200, 433, 1000} // stuck to the left edge
	from := point{100, 210}
	// A few pixels away: still stuck.
	if got := stick(dragged(start, from, point{108, 210}), area, 16); got.left != -7 {
		t.Errorf("8 px away: left = %d, want still at the edge", got.left)
	}
	// Past the distance: free, exactly where the pointer took it.
	if got := stick(dragged(start, from, point{140, 210}), area, 16); got.left != 33 {
		t.Errorf("40 px away: left = %d, want 33", got.left)
	}
}
