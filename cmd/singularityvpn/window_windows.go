//go:build windows

package main

import (
	"os"
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
)

const (
	monitor_default_to_nearest = 2
	swp_no_z_order             = 0x0004
	swp_no_activate            = 0x0010
)

type win_rect struct{ left, top, right, bottom int32 }

type monitor_info struct {
	size    uint32
	monitor win_rect
	work    win_rect
	flags   uint32
}

// main_window finds this process's visible Wails window.
func main_window() uintptr {
	pid := uint32(os.Getpid())
	var found uintptr
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var owner uint32
		windows.GetWindowThreadProcessId(windows.HWND(hwnd), &owner)
		if owner != pid {
			return 1
		}
		if visible, _, _ := proc_is_visible.Call(hwnd); visible == 0 {
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
	left, right := fit_width(window, info.work, int32(width)*int32(dpi)/96)
	proc_set_window_pos.Call(hwnd, 0, uintptr(left), uintptr(window.top),
		uintptr(right-left), uintptr(window.bottom-window.top), swp_no_z_order|swp_no_activate)
}
