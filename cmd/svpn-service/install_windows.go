//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"singularity-vpn-app/internal/platform/windows/divert"
)

const (
	display_name = "Singularity VPN"
	description  = "Owns the Singularity VPN tunnel: the network adapter, the routes, the DNS and the connection to the control plane."
)

// uninstall_wait bounds how long uninstall waits for a running service to
// stop. It is above the stop's own wait hint, because that stop is taking
// routes and DNS back off the machine and cutting it short is the worse
// outcome.
const uninstall_wait = 60 * time.Second

// require_administrator refuses before anything is touched, naming what is
// needed: a service control manager error halfway through is a worse way
// to find out.
func require_administrator(command string) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		fmt.Fprintf(os.Stderr, "svpn-service %s: this needs Administrator. Re-run from an elevated prompt.\n", command)
		os.Exit(1)
	}
}

func fail(command string, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "svpn-service %s: %s\n", command, fmt.Sprintf(format, args...))
	os.Exit(1)
}

// executable_path is the absolute, symlink-resolved path the service will
// be registered with. The service control manager records the path as
// given, and a relative one would be resolved from C:\Windows\System32 at
// every start.
func executable_path() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// inside_program_files refuses a service binary anywhere an ordinary user
// can write. The service runs as LocalSystem, so whoever can replace this
// file -- any program running as the person, unelevated -- runs as
// LocalSystem at the next start. Program Files is writable only by
// administrators, which is why services live there. Stage 8's installer
// puts it there; until then, copying it there by hand is the price of
// installing it at all.
func inside_program_files(path string) (bool, string, error) {
	program_files, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return false, "", err
	}
	relative, err := filepath.Rel(program_files, path)
	if err != nil {
		return false, program_files, nil
	}
	return relative != "." && !strings.HasPrefix(relative, ".."), program_files, nil
}

func install() {
	require_administrator("install")

	path, err := executable_path()
	if err != nil {
		fail("install", "cannot tell where this executable is: %v", err)
	}
	safe, program_files, err := inside_program_files(path)
	if err != nil {
		fail("install", "cannot locate Program Files: %v", err)
	}
	if !safe {
		fail("install", "refusing to register %s as a LocalSystem service: that directory is not under Program Files, "+
			"so a program running as an ordinary user may be able to replace the file and run as LocalSystem. "+
			"Copy svpn-service.exe and wintun.dll into \"%s\\Singularity VPN\\\" and run install from there.",
			path, program_files)
	}

	manager, err := mgr.Connect()
	if err != nil {
		fail("install", "connecting to the service control manager: %v", err)
	}
	defer manager.Disconnect()

	if existing, err := manager.OpenService(service_name); err == nil {
		existing.Close()
		// Reconfiguring a service that may be running and holding a
		// tunnel is not what install was asked to do.
		fail("install", "the service %s already exists; run uninstall first", service_name)
	}

	service, err := manager.CreateService(service_name, path, mgr.Config{
		DisplayName: display_name,
		Description: description,
		// Automatic and not delayed: the point of the service is that a
		// machine comes back connected, and a delayed start is a window
		// with traffic outside the tunnel.
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: false,
		// Empty means LocalSystem, which creating a network adapter and
		// writing the routing table require.
		ServiceStartName: "",
	})
	if err != nil {
		fail("install", "creating the service: %v", err)
	}
	defer service.Close()

	// A VPN service that dies and stays dead leaves a person disconnected
	// with no sign of it. Non-crash failures count too: losing the pipe
	// ends the service with an error code rather than a crash, and it is
	// exactly the failure a restart fixes.
	restarts := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	if err := service.SetRecoveryActions(restarts, uint32((24 * time.Hour).Seconds())); err != nil {
		fmt.Fprintf(os.Stderr, "svpn-service install: warning: setting the restart-on-failure actions: %v\n", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		fmt.Fprintf(os.Stderr, "svpn-service install: warning: applying restarts to non-crash failures: %v\n", err)
	}

	if err := eventlog.InstallAsEventCreate(service_name, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		fmt.Fprintf(os.Stderr, "svpn-service install: warning: registering the event log source: %v\n", err)
	}

	fmt.Printf("installed %s, starting automatically, as LocalSystem, from:\n  %s\n", service_name, path)
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "wintun.dll")); err != nil {
		fmt.Println("warning: wintun.dll is not beside it; the tunnel cannot come up until it is there or in System32.")
	}
	fmt.Println("start it now with:  sc start " + service_name)
}

func uninstall() {
	require_administrator("uninstall")

	manager, err := mgr.Connect()
	if err != nil {
		fail("uninstall", "connecting to the service control manager: %v", err)
	}
	defer manager.Disconnect()

	service, err := manager.OpenService(service_name)
	if err != nil {
		fail("uninstall", "the service %s is not installed: %v", service_name, err)
	}
	defer service.Close()

	status, err := service.Query()
	if err != nil {
		fail("uninstall", "asking the service for its state: %v", err)
	}
	if status.State != svc.Stopped {
		fmt.Println("stopping the service; it takes the tunnel down first...")
		if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			fail("uninstall", "asking the service to stop: %v", err)
		}
		deadline := time.Now().Add(uninstall_wait)
		for {
			status, err = service.Query()
			if err != nil {
				fail("uninstall", "asking the service for its state: %v", err)
			}
			if status.State == svc.Stopped {
				break
			}
			if time.Now().After(deadline) {
				// Deleting a service that is still tearing a tunnel down
				// would only mark it for deletion and leave it running.
				fail("uninstall", "the service did not stop within %s; nothing was removed. Check the log, then try again.", uninstall_wait)
			}
			time.Sleep(500 * time.Millisecond)
		}
		fmt.Println("stopped.")
	}

	if err := service.Delete(); err != nil {
		fail("uninstall", "removing the service: %v", err)
	}
	if err := eventlog.Remove(service_name); err != nil {
		fmt.Fprintf(os.Stderr, "svpn-service uninstall: warning: removing the event log source: %v\n", err)
	}

	paths, _ := service_paths()
	fmt.Printf("removed the service %s.\n", service_name)
	if paths.root != "" {
		remove_packet_driver(paths)
	}
	// The data is kept on purpose: the device token and the profile id are
	// there, and uninstalling to reinstall is not a request to spend
	// another of the account's profile slots.
	if paths.root != "" {
		fmt.Printf("kept %s -- it holds this machine's sign-in and its VPN profile; a reinstall picks them up again.\n", paths.root)
	}
}

// remove_packet_driver takes the WinDivert driver off the machine if this
// program installed it, and leaves one another program installed. The
// service has stopped, so nothing of ours still holds it open. A failure
// is a warning: the service itself is already gone.
func remove_packet_driver(p paths) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "svpn-service uninstall: warning: locating this program to remove the WinDivert driver: %v\n", err)
		return
	}
	removal, err := divert.New_driver(filepath.Join(p.root, "windivert.ours"), filepath.Dir(exe)).Remove()
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "svpn-service uninstall: warning: removing the WinDivert driver: %v\n", err)
	case removal == divert.Removal_removed:
		fmt.Println("removed the WinDivert packet driver.")
	case removal == divert.Removal_foreign:
		fmt.Println("left the WinDivert packet driver in place: another program installed it.")
	}
}

// query reports what the service control manager says, with no
// administrator required. It asks for the least access that answers the
// question -- mgr.Connect and OpenService ask for all of it, which an
// ordinary user is refused.
func query() {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		fail("status", "connecting to the service control manager: %v", err)
	}
	defer windows.CloseServiceHandle(manager)

	name, _ := windows.UTF16PtrFromString(service_name)
	service, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			fmt.Printf("%s is not installed.\n", service_name)
			return
		}
		fail("status", "opening the service: %v", err)
	}
	defer windows.CloseServiceHandle(service)

	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(service, &status); err != nil {
		fail("status", "asking the service for its state: %v", err)
	}
	fmt.Printf("%s: %s\n", service_name, state_name(svc.State(status.CurrentState)))
}

func state_name(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "resuming"
	case svc.PausePending:
		return "pausing"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("state %d", state)
	}
}
