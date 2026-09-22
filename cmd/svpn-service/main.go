//go:build windows

// Command svpn-service is the Windows service that owns the tunnel, the
// routes, the DNS and every call to the control plane. It runs as
// LocalSystem and speaks to the interface only through the pipe in
// internal/ipc, whose command set is closed: WINDOWS_CLIENT.md 45 forbids
// this process from ever becoming a general executor of privileged
// commands, so it takes no path, address or command from its caller beyond
// that set.
package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
	"golang.org/x/sys/windows/svc/eventlog"
)

func main() {
	if len(os.Args) < 2 {
		is_service, err := svc.IsWindowsService()
		if err != nil {
			fmt.Fprintf(os.Stderr, "svpn-service: cannot tell how this process was started: %v\n", err)
			os.Exit(1)
		}
		if !is_service {
			// Somebody double-clicked the binary. A Windows error code from
			// a service control manager that is not there is not an answer
			// for them; the usage is.
			usage()
			os.Exit(2)
		}
		run_as_service()
		return
	}

	switch os.Args[1] {
	case "run":
		run_in_console()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Singularity VPN service.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  svpn-service run    run in this console until Ctrl+C, for diagnosis (needs Administrator)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Started with no argument, it expects to be run by the service control manager.")
}

func run_as_service() {
	elog, err := eventlog.Open(service_name)
	if err != nil {
		os.Exit(1)
	}
	defer elog.Close()

	if err := svc.Run(service_name, &handler{elog: elog}); err != nil {
		elog.Error(1, "Singularity VPN could not run as a service: "+err.Error())
		os.Exit(1)
	}
}

// run_in_console is the same handler under svc/debug, which turns Ctrl+C
// into the Stop request the service control manager would send. That is
// how the whole path -- pipe, application, tunnel, teardown -- is exercised
// from an elevated console without installing anything.
func run_in_console() {
	if !windows.GetCurrentProcessToken().IsElevated() {
		fmt.Fprintln(os.Stderr, "svpn-service run: this needs Administrator -- it creates a network adapter and changes routes and DNS. Re-run from an elevated prompt.")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "running in this console; press Ctrl+C to stop and tear down.")
	// debug.Run hands back the handler's service-specific exit code dressed
	// as a Windows error, whose text ("Incorrect function.") would be read
	// as the reason. The real reason has already been printed above.
	if err := debug.Run(service_name, &handler{elog: debug.New(service_name), console: true}); err != nil {
		fmt.Fprintln(os.Stderr, "svpn-service run: stopped with a failure; the reason is above.")
		os.Exit(1)
	}
}
