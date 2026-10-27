//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// os_version is the Windows version as major.minor.build, e.g. 10.0.26300,
// for a problem report. RtlGetVersion is asked rather than GetVersionEx, which
// reports a lie to a program without a manifest saying which Windows it
// knows. Empty when it cannot be read: the report says nothing rather than
// guessing.
func os_version() string {
	info := windows.RtlGetVersion()
	if info == nil || info.MajorVersion == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", info.MajorVersion, info.MinorVersion, info.BuildNumber)
}
