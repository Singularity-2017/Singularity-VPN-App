//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	app_dir_name  = "Singularity VPN"
	service_name  = "SingularityVPN"
	run_key       = `Software\Microsoft\Windows\CurrentVersion\Run`
	run_value     = "Singularity VPN"
	uninstall_key = `Software\Microsoft\Windows\CurrentVersion\Uninstall\SingularityVPN`
	shortcut_name = "Singularity VPN.lnk"
	service_wait  = 60 * time.Second
)

func install_dir() (string, error) {
	program_files, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", err
	}
	return filepath.Join(program_files, app_dir_name), nil
}

func data_dir() (string, error) {
	program_data, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", err
	}
	return filepath.Join(program_data, app_dir_name), nil
}

func shortcut_path() (string, error) {
	programs, err := windows.KnownFolderPath(windows.FOLDERID_CommonPrograms, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", err
	}
	return filepath.Join(programs, shortcut_name), nil
}

// install_security is the install directory's whole descriptor. Inherited
// Program Files permissions are not enough on their own: an object an
// elevated administrator creates is owned, on a stock install, by that
// person's own account, and an owner may always rewrite the DACL -- so any
// program they run unelevated could make svpn-service.exe writable and
// replace it, and be LocalSystem at the next start. Hence:
//
//	O:BA                   Administrators own it, set explicitly
//	D:P                    protected: nothing inherited from above
//	(A;OICI;FA;;;SY)       LocalSystem: full control
//	(A;OICI;FA;;;BA)       Administrators: full control
//	(A;OICI;0x1200a9;;;BU) Users: read and execute -- the window runs from here
//	(A;OICI;RC;;;OW)       Owner Rights: only read the descriptor, which takes
//	                       away the owner's implicit WRITE_DAC on the directory
//
// On files, both layers were observed (2026-10-19): a file written here by
// an elevated administrator is owned by that person's account and inherits
// the entry as effective, (A;ID;RC;;;OW), so the account has no WRITE_DAC;
// write_replacing then makes Administrators the owner, after which Windows
// marks the entry inherit-only, (A;IOID;RC;;;OW), and the owner's implicit
// rights belong to Administrators. Either alone closes the hole.
const install_security = `O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)(A;OICI;RC;;;OW)`

// secure_install_dir applies install_security to dir, existing or new; the
// inheritable entries reach the files already in it.
func secure_install_dir(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(install_security)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
}

// own_by_administrators makes Administrators the owner of path.
func own_by_administrators(path string) error {
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, administrators, nil, nil, nil)
}

// write_replacing writes data beside path and moves it over path in one
// step, so a failure halfway leaves the old file, not half of a new one.
// The move is retried for a few seconds: a process can keep its image
// locked for a moment after the service manager reports it stopped.
func write_replacing(path string, data []byte) error {
	temp := path + ".new"
	if err := os.WriteFile(temp, data, 0o755); err != nil {
		return err
	}
	if err := own_by_administrators(temp); err != nil {
		os.Remove(temp)
		return fmt.Errorf("setting the owner: %w", err)
	}
	from, _ := windows.UTF16PtrFromString(temp)
	to, _ := windows.UTF16PtrFromString(path)
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Still locked: a program is running from it -- the window, during a
	// quiet update. A running image cannot be overwritten but can be
	// renamed; it is moved aside, the new file takes its name, and the old
	// one goes when it is no longer running, or at the next start.
	aside := path + ".old"
	os.Remove(aside)
	aside_ptr, _ := windows.UTF16PtrFromString(aside)
	if windows.MoveFileEx(to, aside_ptr, windows.MOVEFILE_REPLACE_EXISTING) == nil {
		if err = windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH); err == nil {
			if os.Remove(aside) != nil {
				delete_at_reboot(aside)
			}
			return nil
		}
		windows.MoveFileEx(aside_ptr, to, 0) // put the old one back
	}
	os.Remove(temp)
	return err
}

// delete_at_reboot asks Windows to remove path at the next start -- for the
// uninstaller itself, which cannot delete the file it is running from.
func delete_at_reboot(path string) error {
	p, _ := windows.UTF16PtrFromString(path)
	return windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

// close_windows_from ends every running copy of the window, and of
// svpn-ctl, started from dir, in any session: its file is about to be replaced or removed, and a
// running image cannot be. Other programs, and copies run from anywhere
// else, are left alone -- the full path is compared, not the name.
func close_windows_from(dir string) (int, error) {
	targets := []string{filepath.Join(dir, window_exe), filepath.Join(dir, ctl_exe)}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	closed := 0
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if !strings.EqualFold(name, window_exe) && !strings.EqualFold(name, ctl_exe) {
			continue
		}
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, entry.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_PATH)
		size := uint32(len(buf))
		image := ""
		if windows.QueryFullProcessImageName(process, 0, &buf[0], &size) == nil {
			image = windows.UTF16ToString(buf[:size])
		}
		if image != "" && (strings.EqualFold(image, targets[0]) || strings.EqualFold(image, targets[1])) {
			if windows.TerminateProcess(process, 0) == nil {
				windows.WaitForSingleObject(process, 5000)
				closed++
			}
		}
		windows.CloseHandle(process)
	}
	return closed, nil
}

// stop_service stops the service if it exists and is running, and waits
// for it: stopping takes the tunnel's routes and DNS off the machine, and
// replacing its files before that has finished would leave them there.
// It reports whether the service exists at all.
func stop_service() (bool, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return false, err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(service_name)
	if err != nil {
		return false, nil
	}
	defer service.Close()

	status, err := service.Query()
	if err != nil {
		return true, err
	}
	if status.State == svc.Stopped {
		return true, nil
	}
	if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return true, err
	}
	deadline := time.Now().Add(service_wait)
	for {
		status, err = service.Query()
		if err != nil {
			return true, err
		}
		if status.State == svc.Stopped {
			return true, nil
		}
		if time.Now().After(deadline) {
			return true, fmt.Errorf("the service did not stop within %s", service_wait)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// point_service_at makes an existing service run binary, in case an older
// installation registered it from somewhere else.
func point_service_at(binary string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(service_name)
	if err != nil {
		return err
	}
	defer service.Close()
	config, err := service.Config()
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.Trim(config.BinaryPathName, `"`), binary) {
		return nil
	}
	config.BinaryPathName = `"` + binary + `"`
	return service.UpdateConfig(config)
}

func service_running() bool {
	manager, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(service_name)
	if err != nil {
		return false
	}
	defer service.Close()
	status, err := service.Query()
	return err == nil && (status.State == svc.Running || status.State == svc.StartPending)
}

func start_service() error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(service_name)
	if err != nil {
		return err
	}
	defer service.Close()
	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return err
	}
	return nil
}

func set_autostart(dir string) error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, run_key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(run_value, autostart_command(dir))
}

func remove_autostart() error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, run_key, registry.SET_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	if err := key.DeleteValue(run_value); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func write_uninstall_entry(values []reg_value) error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstall_key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	for _, v := range values {
		if v.is_dw {
			err = key.SetDWordValue(v.name, v.number)
		} else {
			err = key.SetStringValue(v.name, v.text)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", v.name, err)
		}
	}
	return nil
}

func remove_uninstall_entry() error {
	if err := registry.DeleteKey(registry.LOCAL_MACHINE, uninstall_key); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// create_shortcut puts the window in the Start menu for every user,
// through the shell's own WScript.Shell object.
func create_shortcut(path, target, dir string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		return err
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return err
	}
	defer unknown.Release()
	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer shell.Release()

	created, err := oleutil.CallMethod(shell, "CreateShortcut", path)
	if err != nil {
		return err
	}
	link := created.ToIDispatch()
	defer link.Release()
	for name, value := range map[string]string{
		"TargetPath":       target,
		"WorkingDirectory": dir,
		"Description":      "Singularity VPN",
	} {
		if _, err := oleutil.PutProperty(link, name, value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	_, err = oleutil.CallMethod(link, "Save")
	return err
}
