//go:build windows

// Command svpn-divert is the console harness for stage 9a: it loads the
// WinDivert driver the way the service will, shows which process owns each
// socket as it is bound or connected, and takes the driver off the machine
// again. It needs administrator, and it changes the machine: the first
// watch installs the WinDivert driver service.
//
//	svpn-divert watch [-seconds N]   install if needed, print socket events
//	svpn-divert owners [-seconds N]  build the port owner table, print it
//	svpn-divert status               is the driver there, and is it ours
//	svpn-divert remove               stop and delete it, if it is ours
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"singularity-vpn-app/internal/games"
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
	case "watch", "owners":
		flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
		seconds := flags.Int("seconds", 30, "how long to watch; Ctrl+C stops sooner")
		_ = flags.Parse(os.Args[2:])
		duration := time.Duration(*seconds) * time.Second
		if os.Args[1] == "watch" {
			err = watch(driver, duration)
		} else {
			err = owners(driver, duration)
		}
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
	fmt.Fprintln(os.Stderr, "usage: svpn-divert watch [-seconds N] | owners [-seconds N] | status | remove")
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
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return divert.New_driver(filepath.Join(dir, "windivert.ours"), filepath.Dir(exe)), nil
}

// open_sockets opens a socket-layer handle the way the service will, and
// shuts it down after duration or on Ctrl+C.
func open_sockets(driver *divert.Driver, duration time.Duration) (*divert.Handle, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	library, err := divert.Load(filepath.Dir(exe))
	if err != nil {
		return nil, err
	}
	// The socket layer must sniff: without it every socket event would be
	// blocked, and the machine would lose its network for the duration.
	var handle *divert.Handle
	ours, err := driver.Open(func() (err error) {
		handle, err = library.Open("true", divert.Layer_socket, 0, divert.Flag_sniff|divert.Flag_recv_only)
		return err
	})
	if err != nil {
		return nil, err
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
	return handle, nil
}

// each_event hands every socket event to handle until the handle is shut
// down, then closes it.
func each_event(handle *divert.Handle, on_event func(event int, socket divert.Socket)) error {
	for {
		var address divert.Address
		if _, err := handle.Recv(nil, &address); err != nil {
			if errors.Is(err, windows.ERROR_NO_DATA) {
				break
			}
			_ = handle.Close()
			return err
		}
		on_event(address.Event(), address.Socket())
	}
	if err := handle.Close(); err != nil {
		return err
	}
	fmt.Println("stopped; the driver stays loaded until svpn-divert remove")
	return nil
}

func watch(driver *divert.Driver, duration time.Duration) error {
	handle, err := open_sockets(driver, duration)
	if err != nil {
		return err
	}
	names := image_names{}
	return each_event(handle, func(event int, socket divert.Socket) {
		fmt.Printf("%-8s %-4s pid %-6d %-24s %s -> %s\n",
			event_name(event), protocol_name(socket.Protocol), socket.Process_id, names.of(socket.Process_id),
			endpoint(socket.Local_address, socket.Local_port), endpoint(socket.Remote_address, socket.Remote_port))
	})
}

// owners fills the table the way the service will -- the system's socket
// tables first, then events -- and prints who owns what at the end, with
// the catalog game each process belongs to.
func owners(driver *divert.Driver, duration time.Duration) error {
	catalog, err := games.Load_catalog()
	if err != nil {
		return err
	}
	table := divert.New_owners()
	handle, err := open_sockets(driver, duration)
	if err != nil {
		return err
	}
	// The snapshot is read after the handle opens, so a socket opened in
	// between is in one or the other and never in neither.
	existing, err := divert.Existing_sockets()
	if err != nil {
		_ = handle.Shutdown()
		_ = handle.Close()
		return err
	}
	table.Replace_snapshot(existing)
	fmt.Printf("%d sockets were already open\n", len(existing))
	events := 0
	if err := each_event(handle, func(event int, socket divert.Socket) {
		events++
		table.Apply(event, socket)
	}); err != nil {
		return err
	}
	fmt.Printf("%d events applied\n\n", events)

	names := image_names{}
	for _, pid := range table.Processes() {
		name := names.of(pid)
		game := ""
		if found, ok := catalog.By_image(name); ok {
			game = "  [" + found.Id + "]"
		}
		ports := table.Ports_of(func(p uint32) bool { return p == pid })
		texts := make([]string, 0, len(ports))
		for _, port := range ports {
			texts = append(texts, fmt.Sprintf("%s/%d", port.Protocol, port.Number))
		}
		fmt.Printf("pid %-6d %-24s%s %s\n", pid, name, game, strings.Join(shorten(texts, 12), " "))
	}
	return nil
}

// shorten keeps the first n items and says how many it left out.
func shorten(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return append(slices.Clone(items[:n]), fmt.Sprintf("(+%d more)", len(items)-n))
}

func endpoint(address netip.Addr, port uint16) string {
	if !address.IsValid() {
		return fmt.Sprintf("?:%d", port)
	}
	return netip.AddrPortFrom(address, port).String()
}

// image_names caches each process's executable name.
type image_names map[uint32]string

func (names image_names) of(pid uint32) string {
	name, seen := names[pid]
	if !seen {
		name = image_name(pid)
		names[pid] = name
	}
	return name
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
