//go:build windows

package core

import (
	"os"

	"golang.org/x/sys/windows"
)

// open_shared opens a file for reading while the service goes on appending to
// it, and goes on rolling it over. Go's own os.Open leaves out
// FILE_SHARE_DELETE, so a rename of the log by the writer would fail for as
// long as this handle is open.
func open_shared(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
