//go:build windows

package divert

import (
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"singularity-vpn-app/internal/games"
	"singularity-vpn-app/internal/split"
)

// Router is the Windows game router: it keeps the port owner table from
// the socket layer, captures outbound packets from the selected games'
// ports and hands them to the tunnel, and injects the tunnel's replies
// inbound on the interface each flow left from. It implements
// wireguard.Game_router.
type Router struct {
	dir    string
	driver *Driver
	logger *slog.Logger

	mu      sync.Mutex
	library *Library
	sockets *Handle
	// sender is guarded by its own lock, held across each Send: Deliver
	// runs on wireguard-go's goroutines, and Stop must not close the
	// handle under a Send in flight.
	send_mu sync.RWMutex
	sender  *Handle
	stop    chan struct{}
	done    sync.WaitGroup
}

// The router's timing. A burst of socket events -- a game starting opens
// dozens -- is folded into one filter rebuild after settle. The system's
// tables are read again every refresh, which also forgets cached image
// names, since a process id can be reused.
const (
	settle  = 250 * time.Millisecond
	refresh = 30 * time.Second
)

// New_router builds a router that loads WinDivert.dll from dir and
// installs the driver through driver, so an installation is remembered as
// ours.
func New_router(dir string, driver *Driver, logger *slog.Logger) *Router {
	return &Router{dir: dir, driver: driver, logger: logger}
}

func (r *Router) Start(selected []games.Game, splitter *split.Splitter, inject func(packet []byte) bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop != nil {
		return errors.New("divert: the game router is already running")
	}
	if r.library == nil {
		library, err := Load(r.dir)
		if err != nil {
			return err
		}
		r.library = library
	}
	var sockets *Handle
	ours, err := r.driver.Open(func() (err error) {
		sockets, err = r.library.Open("true", Layer_socket, 0, Flag_sniff|Flag_recv_only)
		return err
	})
	if err != nil {
		return err
	}
	if !ours {
		r.logger.Info("the WinDivert driver was installed by another program; it will be left in place")
	}
	sender, err := r.library.Open("false", Layer_network, 0, Flag_send_only)
	if err != nil {
		_ = sockets.Shutdown()
		_ = sockets.Close()
		return err
	}
	existing, err := Existing_sockets()
	if err != nil {
		_ = sockets.Shutdown()
		_ = sockets.Close()
		_ = sender.Close()
		return err
	}

	r.sockets = sockets
	r.send_mu.Lock()
	r.sender = sender
	r.send_mu.Unlock()
	r.stop = make(chan struct{})
	images := map[string]bool{}
	for _, game := range selected {
		for _, image := range game.Images {
			images[strings.ToLower(image)] = true
		}
	}
	events := make(chan socket_event, 1024)
	r.done.Add(2)
	go r.read_sockets(sockets, events, r.stop)
	go r.follow(images, existing, events, splitter, inject, r.stop)
	r.logger.Info("game router started", "games", len(selected), "sockets", len(existing))
	return nil
}

type socket_event struct {
	event  int
	socket Socket
}

// read_sockets passes the socket layer's events on until its handle is
// shut down.
func (r *Router) read_sockets(handle *Handle, events chan<- socket_event, stop <-chan struct{}) {
	defer r.done.Done()
	defer close(events)
	for {
		var address Address
		if _, err := handle.Recv(nil, &address); err != nil {
			if !errors.Is(err, windows.ERROR_NO_DATA) && !errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
				r.logger.Warn("game router: the socket layer stopped", "error", err)
			}
			return
		}
		select {
		case events <- socket_event{address.Event(), address.Socket()}:
		case <-stop:
			return
		}
	}
}

// follow owns the owner table. It applies socket events, re-reads the
// system's tables, and reopens the capture handle whenever the games' port
// set changes: the new handle opens at a higher priority before the old
// one is retired, so no game packet slips out direct in between.
func (r *Router) follow(images map[string]bool, existing []Owned, events <-chan socket_event, splitter *split.Splitter, inject func([]byte) bool, stop <-chan struct{}) {
	defer r.done.Done()
	owners := New_owners()
	owners.Replace_snapshot(existing)
	names := map[uint32]string{}
	is_game := func(pid uint32) bool {
		name, seen := names[pid]
		if !seen {
			name = strings.ToLower(Image_name(pid))
			// A name that could not be read is asked for again next
			// time: a process just starting may not answer yet.
			if name != "" {
				names[pid] = name
			}
		}
		return images[name]
	}

	timer := time.NewTimer(settle)
	timer.Stop()
	var capture *Handle
	var capture_done sync.WaitGroup
	filter := ""
	keep := map[Port]bool{}
	priority := int16(0)
	rebuild := func() {
		ports := owners.Ports_of(is_game)
		keep = map[Port]bool{}
		for _, port := range ports {
			keep[port] = true
		}
		splitter.Retain(func(protocol uint8, number uint16) bool {
			name, ok := protocol_of(protocol)
			return ok && keep[Port{name, number}]
		})
		next, err := Outbound_ports_filter(ports)
		if err != nil || next == filter {
			return
		}
		var replacement *Handle
		if len(ports) > 0 {
			if priority >= Priority_highest {
				priority = 0
			}
			priority++
			replacement, err = r.library.Open(next, Layer_network, priority, 0)
			if err != nil {
				r.logger.Warn("game router: the capture filter was refused", "ports", len(ports), "error", err)
				timer.Reset(4 * settle) // try again, not only at the next event
				return
			}
			capture_done.Add(1)
			go r.capture(replacement, splitter, inject, &capture_done)
		}
		if capture != nil {
			_ = capture.Shutdown_receive()
		}
		capture, filter = replacement, next
		r.logger.Info("game router: capturing", "ports", len(ports))
	}

	ticker := time.NewTicker(refresh)
	defer ticker.Stop()
	rebuild()
	for {
		select {
		case <-stop:
			if capture != nil {
				_ = capture.Shutdown()
			}
			capture_done.Wait()
			return
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			owners.Apply(e.event, e.socket)
			// A game's new port is captured at once: its first packets
			// leave within milliseconds of the bind, and one that went
			// out direct gives the server a second address for the flow.
			// Everything else -- closes, other programs -- waits for the
			// burst to settle.
			if e.event != Event_socket_close && is_game(e.socket.Process_id) {
				if protocol, ok := protocol_of(e.socket.Protocol); ok && !keep[Port{protocol, e.socket.Local_port}] {
					rebuild()
					continue
				}
			}
			timer.Reset(settle)
		case <-timer.C:
			rebuild()
		case <-ticker.C:
			if fresh, err := Existing_sockets(); err == nil {
				owners.Replace_snapshot(fresh)
			}
			clear(names)
			rebuild()
		}
	}
}

// capture takes each diverted game packet into the tunnel. One the
// splitter cannot carry goes out as it came.
func (r *Router) capture(handle *Handle, splitter *split.Splitter, inject func([]byte) bool, done *sync.WaitGroup) {
	defer done.Done()
	defer handle.Close()
	buffer := make([]byte, 65535)
	for {
		var address Address
		n, err := handle.Recv(buffer, &address)
		if err != nil {
			return
		}
		packet := buffer[:n]
		index, sub_index := address.Interface()
		if splitter.Outbound(packet, split.Origin{Interface_index: index, Sub_index: sub_index}) {
			inject(packet)
			continue
		}
		_ = handle.Send(packet, &address)
	}
}

func (r *Router) Deliver(packet []byte, origin split.Origin) bool {
	r.send_mu.RLock()
	defer r.send_mu.RUnlock()
	if r.sender == nil {
		return false
	}
	var address Address
	address.Set_outbound(false)
	address.Set_interface(origin.Interface_index, origin.Sub_index)
	address.Set_checksums_valid()
	return r.sender.Send(packet, &address) == nil
}

func (r *Router) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop == nil {
		return nil
	}
	close(r.stop)
	shutdown_err := r.sockets.Shutdown()
	r.done.Wait()
	close_err := r.sockets.Close()
	r.send_mu.Lock()
	sender_err := r.sender.Close()
	r.sender = nil
	r.send_mu.Unlock()
	r.stop, r.sockets = nil, nil
	r.logger.Info("game router stopped")
	return errors.Join(shutdown_err, close_err, sender_err)
}

func (r *Router) Remove_driver() error {
	// A handle that failed to close is reported, but removal is still
	// tried: it fails on its own if the driver is still held, and a
	// touchy anti-cheat must not be left facing the driver for less.
	if err := r.Stop(); err != nil {
		r.logger.Warn("game router: stopping before removing the driver", "error", err)
	}
	removal, err := r.driver.Remove()
	if err != nil {
		return err
	}
	switch removal {
	case Removal_removed:
		r.logger.Info("the WinDivert driver was removed")
	case Removal_foreign:
		r.logger.Info("the WinDivert driver belongs to another program and was left in place")
	}
	return nil
}

// Image_name is the executable name of a process, or "" when it cannot be
// read -- a process that has exited, or one this account may not query.
func Image_name(pid uint32) string {
	if pid == 0 {
		return ""
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buffer[:size]))
}
