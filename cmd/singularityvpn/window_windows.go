//go:build windows

package main

import (
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Wails runtime reads the window position in physical pixels of the
// whole desktop, sets it relative to the monitor's work area, and sizes in
// pixels at 96 DPI; widening the window from the page with those calls
// cannot keep it on the screen. This does it in one place, in physical
// pixels throughout.

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	proc_enum_windows     = user32.NewProc("EnumWindows")
	proc_get_window_rect  = user32.NewProc("GetWindowRect")
	proc_set_window_pos   = user32.NewProc("SetWindowPos")
	proc_monitor_from_win = user32.NewProc("MonitorFromWindow")
	proc_get_monitor_info = user32.NewProc("GetMonitorInfoW")
	proc_get_dpi_for_win  = user32.NewProc("GetDpiForWindow")
	proc_is_visible       = user32.NewProc("IsWindowVisible")
	proc_get_class_name   = user32.NewProc("GetClassNameW")
	proc_set_window_long  = user32.NewProc("SetWindowLongPtrW")
	proc_call_window_proc = user32.NewProc("CallWindowProcW")
	proc_get_cursor_pos   = user32.NewProc("GetCursorPos")

	dwmapi                 = windows.NewLazySystemDLL("dwmapi.dll")
	proc_dwm_get_attribute = dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	monitor_default_to_nearest  = 2
	swp_no_z_order              = 0x0004
	swp_no_activate             = 0x0010
	gwlp_wndproc                = ^uintptr(3) // -4
	wm_moving                   = 0x0216
	wm_enter_size_move          = 0x0231
	dwmwa_extended_frame_bounds = 9

	// stick_distance is how near an edge of the work area, in pixels at
	// 96 DPI, the window's visible edge snaps onto it while dragged.
	stick_distance = 16
)

type win_rect struct{ left, top, right, bottom int32 }

type monitor_info struct {
	size    uint32
	monitor win_rect
	work    win_rect
	flags   uint32
}

// main_window finds this process's visible Wails window.
func main_window() uintptr { return find_window(true) }

// find_window finds this process's Wails window, visible or, when
// visible is false, not yet shown.
func find_window(visible bool) uintptr {
	pid := uint32(os.Getpid())
	var found uintptr
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var owner uint32
		windows.GetWindowThreadProcessId(windows.HWND(hwnd), &owner)
		if owner != pid {
			return 1
		}
		if shown, _, _ := proc_is_visible.Call(hwnd); visible && shown == 0 {
			return 1
		}
		name := make([]uint16, 64)
		proc_get_class_name.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		if windows.UTF16ToString(name) != "wailsWindow" {
			return 1
		}
		found = hwnd
		return 0
	})
	proc_enum_windows.Call(callback, 0)
	return found
}

// frame_inset is how far the visible frame lies inside the window's
// rectangle: Windows 10 and 11 keep an invisible resize border around a
// framed window, and an edge that should touch the screen's is the
// visible one. Zero where DWM does not say.
func frame_inset(hwnd uintptr, window win_rect) win_rect {
	var visible win_rect
	if hr, _, _ := proc_dwm_get_attribute.Call(hwnd, dwmwa_extended_frame_bounds,
		uintptr(unsafe.Pointer(&visible)), unsafe.Sizeof(visible)); hr != 0 {
		return win_rect{}
	}
	return win_rect{visible.left - window.left, visible.top - window.top, window.right - visible.right, window.bottom - visible.bottom}
}

// outer is the work area grown by the inset: where the window's rectangle
// lies when its visible frame touches the work area's edges.
func outer(work, inset win_rect) win_rect {
	return win_rect{work.left - inset.left, work.top - inset.top, work.right + inset.right, work.bottom + inset.bottom}
}

// stick moves a window being dragged onto any edge of area its own edge
// has come within distance of; area is outer() of the work area, so the
// visible frame is what touches. Past the distance it moves freely: the
// rectangle Windows proposes follows the pointer, not the last one.
func stick(window, area win_rect, distance int32) win_rect {
	near := func(a, b int32) bool { return a-b < distance && b-a < distance }
	switch {
	case near(window.left, area.left):
		window.right += area.left - window.left
		window.left = area.left
	case near(window.right, area.right):
		window.left += area.right - window.right
		window.right = area.right
	}
	switch {
	case near(window.top, area.top):
		window.bottom += area.top - window.top
		window.top = area.top
	case near(window.bottom, area.bottom):
		window.top += area.bottom - window.bottom
		window.bottom = area.bottom
	}
	return window
}

var sticky struct {
	mu       sync.Mutex
	previous uintptr
	proc     uintptr

	// Where the drag started, window and pointer. The rectangle Windows
	// proposes is the last one plus the pointer's step, so once snapped it
	// would stay snapped: the free position is worked out from these.
	start  win_rect
	cursor point
}

type point struct{ x, y int32 }

// dragged is where the window would be without sticking: where it started,
// moved as far as the pointer has.
func dragged(start win_rect, from, to point) win_rect {
	dx, dy := to.x-from.x, to.y-from.y
	return win_rect{start.left + dx, start.top + dy, start.right + dx, start.bottom + dy}
}

// Stick_to_edges makes the window snap onto the edges of the screen's work
// area while it is dragged. The page calls it once it has loaded; calling
// it again does nothing.
func (a *App) Stick_to_edges() {
	sticky.mu.Lock()
	defer sticky.mu.Unlock()
	if sticky.previous != 0 {
		return
	}
	hwnd := find_window(false)
	if hwnd == 0 {
		return
	}
	if sticky.proc == 0 {
		sticky.proc = syscall.NewCallback(sticky_proc)
	}
	previous, _, _ := proc_set_window_long.Call(hwnd, gwlp_wndproc, sticky.proc)
	sticky.previous = previous
}

func sticky_proc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wm_enter_size_move {
		proc_get_window_rect.Call(hwnd, uintptr(unsafe.Pointer(&sticky.start)))
		proc_get_cursor_pos.Call(uintptr(unsafe.Pointer(&sticky.cursor)))
	}
	if msg == wm_moving && lparam != 0 {
		proposed := *(**win_rect)(unsafe.Pointer(&lparam))
		var now point
		if ok, _, _ := proc_get_cursor_pos.Call(uintptr(unsafe.Pointer(&now))); ok != 0 && sticky.start != (win_rect{}) {
			*proposed = dragged(sticky.start, sticky.cursor, now)
		}
		var current win_rect
		monitor, _, _ := proc_monitor_from_win.Call(hwnd, monitor_default_to_nearest)
		info := monitor_info{size: uint32(unsafe.Sizeof(monitor_info{}))}
		if ok, _, _ := proc_get_window_rect.Call(hwnd, uintptr(unsafe.Pointer(&current))); ok != 0 {
			if ok, _, _ := proc_get_monitor_info.Call(monitor, uintptr(unsafe.Pointer(&info))); ok != 0 {
				dpi, _, _ := proc_get_dpi_for_win.Call(hwnd)
				if dpi == 0 {
					dpi = 96
				}
				area := outer(info.work, frame_inset(hwnd, current))
				*proposed = stick(*proposed, area, stick_distance*int32(dpi)/96)
			}
		}
	}
	result, _, _ := proc_call_window_proc.Call(sticky.previous, hwnd, msg, wparam, lparam)
	return result
}

// fit_width places a window of the new width so that it stays inside the
// work area: it keeps its left edge where it can, moves left as far as the
// right edge needs, and never past the work area's left edge.
func fit_width(window, work win_rect, width int32) (left, right int32) {
	left = window.left
	if left+width > work.right {
		left = work.right - width
	}
	if left < work.left {
		left = work.left
	}
	right = left + width
	if right > work.right {
		right = work.right
	}
	return left, right
}

// place_width is fit_width with the two things a person sees: a window
// that touches the right edge keeps touching it, and a window narrowed
// back goes to where it stood before it was widened (from), if it still
// fits there.
func place_width(window, area win_rect, width int32, from *int32) (left, right int32) {
	if window.right == area.right {
		return fit_width(win_rect{left: area.right - width, right: area.right}, area, width)
	}
	if from != nil && width < window.right-window.left {
		return fit_width(win_rect{left: *from, right: *from + width}, area, width)
	}
	return fit_width(window, area, width)
}

// wide_from is where the window's left edge stood before the detailed mode
// widened it, so narrowing it puts it back.
var wide_from struct {
	mu   sync.Mutex
	left *int32
}

// Set_window_width resizes the window to the given width in pixels at 96
// DPI, keeping it on the monitor it is on. The page calls it to open and
// close the detailed mode.
func (a *App) Set_window_width(width int) {
	if width <= 0 {
		return
	}
	hwnd := main_window()
	if hwnd == 0 {
		return
	}
	var window win_rect
	if ok, _, _ := proc_get_window_rect.Call(hwnd, uintptr(unsafe.Pointer(&window))); ok == 0 {
		return
	}
	monitor, _, _ := proc_monitor_from_win.Call(hwnd, monitor_default_to_nearest)
	info := monitor_info{size: uint32(unsafe.Sizeof(monitor_info{}))}
	if ok, _, _ := proc_get_monitor_info.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return
	}
	dpi, _, _ := proc_get_dpi_for_win.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	target := int32(width) * int32(dpi) / 96
	area := outer(info.work, frame_inset(hwnd, window))
	wide_from.mu.Lock()
	left, right := place_width(window, area, target, wide_from.left)
	if target > window.right-window.left {
		was := window.left
		wide_from.left = &was
	} else {
		wide_from.left = nil
	}
	wide_from.mu.Unlock()
	proc_set_window_pos.Call(hwnd, 0, uintptr(left), uintptr(window.top),
		uintptr(right-left), uintptr(window.bottom-window.top), swp_no_z_order|swp_no_activate)
}
