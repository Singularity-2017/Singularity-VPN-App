//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// shell_execute_info is SHELLEXECUTEINFOW. golang.org/x/sys/windows has
// ShellExecute but not the Ex form, and only the Ex form hands back the
// process to wait for.
type shell_execute_info struct {
	size       uint32
	mask       uint32
	hwnd       windows.Handle
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	inst_app   windows.Handle
	id_list    uintptr
	class      *uint16
	key_class  windows.Handle
	hot_key    uint32
	icon       windows.Handle
	process    windows.Handle
}

const (
	see_mask_nocloseprocess = 0x00000040
	see_mask_noasync        = 0x00000100
)

var shell_execute_ex = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// err_declined is the person answering No to the elevation prompt.
var err_declined = errors.New("the administrator prompt was declined")

// run_elevated starts this executable again through the elevation prompt
// with args, waits for it and returns its exit code. The elevated copy gets
// a console of its own.
func run_elevated(args []string) (uint32, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(self)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	info := shell_execute_info{
		mask:       see_mask_nocloseprocess | see_mask_noasync,
		verb:       verb,
		file:       file,
		parameters: params,
		show:       windows.SW_SHOWNORMAL,
	}
	info.size = uint32(unsafe.Sizeof(info))
	if r, _, call_err := shell_execute_ex.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(call_err, windows.ERROR_CANCELLED) {
			return 0, err_declined
		}
		return 0, call_err
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// launch_unelevated starts the window as the person logged in, never as
// administrator, even when setup itself runs elevated: the shell starts
// what explorer.exe is asked to open with the shell's own, ordinary token.
// The window holds no privilege and must not run with any.
func launch_unelevated(path string) error {
	system_root, err := windows.GetWindowsDirectory()
	if err != nil {
		return err
	}
	return exec.Command(filepath.Join(system_root, "explorer.exe"), path).Start()
}
