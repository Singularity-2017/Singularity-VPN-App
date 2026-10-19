//go:build windows

// Command svpn-divert is the console harness for stage 9a: it loads the
// WinDivert driver the way the service will, shows which process owns each
// socket as it is bound or connected, and takes the driver off the machine
// again. It needs administrator, and it changes the machine: the first
// watch installs the WinDivert driver service.
//
//	svpn-divert watch [-seconds N]   install if needed, print socket events
//	svpn-divert status               is the driver there, and is it ours
//	svpn-divert remove               stop and delete it, if it is ours
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"singularity-vpn-app/internal/platform/windows/divert"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	driver, err := new_driver()
	if err != nil {
		fail(err)
	}
	switch os.Args[1] {
	case "watch":
		flags := flag.NewFlagSet("watch", flag.ExitOnError)
		seconds := flags.Int("seconds", 30, "how long to watch; Ctrl+C stops sooner")
		_ = flags.Parse(os.Args[2:])
		err = watch(driver, time.Duration(*seconds)*time.Second)
	case "status":
		err = status(driver)
	case "remove":
		err = remove(driver)
	default:
		usage()
	}
	if err != nil {
		fail(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: svpn-divert watch [-seconds N] | status | remove")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "svpn-divert:", err)
	os.Exit(1)
}

// new_driver uses the marker the service will use, in its protected data
// directory, so this tool and the service agree on whose driver it is.
func new_driver() (*divert.Driver, error) {
	program_data, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return nil, fmt.Errorf("locating ProgramData: %w", err)
	}
	dir := filepath.Join(program_data, "Singularity VPN")
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("%s is missing -- install the service first, it creates that directory with the right permissions: %w", dir, err)
	}
	return divert.New_driver(filepath.Join(dir, "windivert.ours")), nil
}

func watch(driver *divert.Driver, duration time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	library, err := divert.Load(filepath.Dir(exe))
	if err != nil {
		return err
	}
	// The socket layer must sniff: without it every socket event would be
	// blocked, and the machine would lose its network for the duration.
	var handle *divert.Handle
	ours, err := driver.Open(func() (err error) {
		handle, err = library.Open("true", divert.Layer_socket, 0, divert.Flag_sniff|divert.Flag_recv_only)
		return err
	})
	if err != nil {
		return err
	}
	if !ours {
		fmt.Println("note: the WinDivert driver was installed by another program; remove will leave it")
	}
	fmt.Printf("watching socket events for %s (Ctrl+C stops)\n", duration)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		select {
		case <-stop:
		case <-time.After(duration):
		}
		_ = handle.Shutdown()
	}()

	names := map[uint32]string{}
	for {
		var address divert.Address
		if _, err := handle.Recv(nil, &address); err != nil {
			if errors.Is(err, windows.ERROR_NO_DATA) {
				break
			}
			_ = handle.Close()
			return err
		}
		socket := address.Socket()
		name, seen := names[socket.Process_id]
		if !seen {
			name = image_name(socket.Process_id)
			names[socket.Process_id] = name
		}
		fmt.Printf("%-8s %-4s pid %-6d %-28s local %-5d remote %-5d\n",
			event_name(address.Event()), protocol_name(socket.Protocol), socket.Process_id, name, socket.Local_port, socket.Remote_port)
	}
	if err := handle.Close(); err != nil {
		return err
	}
	fmt.Println("stopped; the driver stays loaded until svpn-divert remove")
	return nil
}

func status(driver *divert.Driver) error {
	exists, ours, err := driver.State()
	if err != nil {
		return err
	}
	switch {
	case !exists:
		fmt.Println("the WinDivert driver is not installed")
	case ours:
		fmt.Println("the WinDivert driver is installed, by this program")
	default:
		fmt.Println("the WinDivert driver is installed, by another program")
	}
	return nil
}

func remove(driver *divert.Driver) error {
	removal, err := driver.Remove()
	if err != nil {
		return err
	}
	switch removal {
	case divert.Removal_absent:
		fmt.Println("nothing to remove: the WinDivert driver is not installed")
	case divert.Removal_removed:
		fmt.Println("removed: the WinDivert driver is stopped and its service deleted")
	case divert.Removal_foreign:
		fmt.Println("left in place: another program installed the WinDivert driver")
	}
	return nil
}

func image_name(pid uint32) string {
	if pid == 0 {
		return "-"
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "?"
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "?"
	}
	return filepath.Base(windows.UTF16ToString(buffer[:size]))
}

func event_name(event int) string {
	switch event {
	case divert.Event_socket_bind:
		return "bind"
	case divert.Event_socket_connect:
		return "connect"
	case divert.Event_socket_listen:
		return "listen"
	case divert.Event_socket_accept:
		return "accept"
	case divert.Event_socket_close:
		return "close"
	}
	return fmt.Sprintf("event%d", event)
}

func protocol_name(protocol uint8) string {
	switch protocol {
	case 6:
		return "tcp"
	case 17:
		return "udp"
	}
	return fmt.Sprintf("%d", protocol)
}
