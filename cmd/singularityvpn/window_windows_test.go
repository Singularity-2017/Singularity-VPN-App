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
