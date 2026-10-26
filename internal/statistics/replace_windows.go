//go:build windows

package statistics

import "golang.org/x/sys/windows"

// The temporary file inherits the protected service directory ACL. MoveFileEx
// replaces the destination on the same volume and waits for the move to flush.
func replace_file(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
