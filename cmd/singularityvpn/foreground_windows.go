//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

// foreground_exe is the base name of the program whose window is in front, or
// "" when there is none or it cannot be read: a window of an elevated or
// protected process refuses a limited query, and that is a gap, not an error.
//
// PROCESS_QUERY_LIMITED_INFORMATION is the least access that names an image,
// and the only one granted for most processes of other sessions or users.
func foreground_exe() string {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return ""
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid == 0 {
		return ""
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(process)

	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return ""
	}
	return base_name(windows.UTF16ToString(buffer[:size]))
}
