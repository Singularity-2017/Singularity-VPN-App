//go:build windows

package divert

import (
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

	counts packet_counts
	// view is the latest port_view follow published, read by capture.
	view atomic.Pointer[port_view]
}

// packet_counts follows game packets along the split path, so the log can
// say where they stop: captured but refused by the tunnel, carried out and
// never answered, answered and not delivered.
type packet_counts struct {
	captured       atomic.Uint64 // diverted by the capture handle
	carried        atomic.Uint64 // handed to the tunnel
	refused        atomic.Uint64 // the tunnel's queue was full or closed
	direct         atomic.Uint64 // not carried, sent back out as it came
	delivered      atomic.Uint64 // replies injected inbound
	deliver_failed atomic.Uint64 // replies the send handle refused
	send_failed    atomic.Uint64 // anybody's packet that could not be sent back out
}

// reset zeroes the counts one by one: a Deliver from the last session's
// tunnel may still be counting.
func (c *packet_counts) reset() {
	for _, n := range []*atomic.Uint64{&c.captured, &c.carried, &c.refused, &c.direct, &c.delivered, &c.deliver_failed, &c.send_failed} {
		n.Store(0)
	}
}

// count_every is how often the router writes its packet counts, while they
// change.
const count_every = 10 * time.Second

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
	// Opened last: from this moment every outbound packet of the machine
	// waits in the driver for capture to read it.
	capture, err := r.library.Open(Outbound_all_filter, Layer_network, 1, 0)
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
	r.counts.reset()
	r.view.Store(nil)
	r.done.Add(3)
	go r.read_sockets(sockets, events, r.stop)
	go r.follow(images, existing, events, capture, splitter, inject, r.stop)
	go r.report(r.stop)
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
// system's tables, publishes which ports are a game's for capture to read,
// and runs capture on the one handle Start opened.
func (r *Router) follow(images map[string]bool, existing []Owned, events <-chan socket_event, capture *Handle, splitter *split.Splitter, inject func([]byte) bool, stop <-chan struct{}) {
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

	// The view is published after every change, so a packet held for an
	// unknown port sees the bind as soon as it is applied. The splitter
	// forgets ports that stopped being a game's only once a burst settles.
	game_count := -1
	publish := func() {
		game := owners.Ports_of(is_game)
		known := owners.Ports_of(func(uint32) bool { return true })
		r.view.Store(new_port_view(game, known))
		if len(game) != game_count {
			game_count = len(game)
			r.logger.Info("game router: game ports", "ports", game_count)
		}
	}
	retain := func() {
		view := r.view.Load()
		splitter.Retain(func(protocol uint8, number uint16) bool {
			name, ok := protocol_of(protocol)
			return ok && view != nil && view.game[Port{name, number}]
		})
	}
	timer := time.NewTimer(settle)
	timer.Stop()
	var capture_done sync.WaitGroup
	guarded := &guarded_handle{handle: capture}
	publish()
	capture_done.Add(1)
	go r.capture(guarded, splitter, inject, &capture_done)

	ticker := time.NewTicker(refresh)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			// Receiving stops; what is already queued -- the machine's
			// ordinary packets among it -- is still read and sent on
			// before the handle closes.
			guarded.shutdown_receive()
			capture_done.Wait()
			return
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			owners.Apply(e.event, e.socket)
			// A burst -- a game starting opens dozens of sockets -- is
			// applied whole and published once.
		drain:
			for {
				select {
				case e, ok := <-events:
					if !ok {
						events = nil
						break drain
					}
					owners.Apply(e.event, e.socket)
				default:
					break drain
				}
			}
			publish()
			timer.Reset(settle)
		case <-timer.C:
			retain()
		case <-ticker.C:
			if fresh, err := Existing_sockets(); err == nil {
				owners.Replace_snapshot(fresh)
			}
			clear(names)
			publish()
			retain()
		}
	}
}

// guarded_handle is the capture handle shared by follow, which stops it,
// and capture, which closes it. Once closed it is never touched again: a
// handle value can be reused by the next one opened.
type guarded_handle struct {
	mu     sync.Mutex
	handle *Handle
	closed bool
}

func (g *guarded_handle) shutdown_receive() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		_ = g.handle.Shutdown_receive()
	}
}

func (g *guarded_handle) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		g.closed = true
		_ = g.handle.Close()
	}
}

// capture sees every outbound packet the filter diverts. A game's goes
// into the tunnel; everything else -- and a game's packet the splitter
// cannot carry -- goes out as it came.
func (r *Router) capture(guarded *guarded_handle, splitter *split.Splitter, inject func([]byte) bool, done *sync.WaitGroup) {
	defer done.Done()
	// Closing is what makes the driver stop diverting: a handle left open
	// with nobody reading would swallow the machine's traffic.
	defer guarded.close()
	handle := guarded.handle
	judge := new_port_judge(r.view.Load)
	buffer := make([]byte, 65535)
	send_error_logged := false
	for {
		var address Address
		n, err := handle.Recv(buffer, &address)
		if err != nil {
			// After a stop this is the queue running dry. Anything else
			// ends capture mid-session: the handle closes, everything --
			// the games too -- goes direct, and the log must say so.
			if !errors.Is(err, windows.ERROR_NO_DATA) && !errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
				r.logger.Warn("game router: capture stopped; games go direct until the next connect", "error", err)
			}
			return
		}
		packet := buffer[:n]
		if flow, ok := split.Flow_of(packet); ok {
			opens := flow.Protocol == split.Protocol_udp || split.Is_syn(packet)
			if name, ok := protocol_of(flow.Protocol); ok && judge.is_game(Port{name, flow.Source.Port()}, opens) {
				r.counts.captured.Add(1)
				index, sub_index := address.Interface()
				if splitter.Outbound(packet, split.Origin{Interface_index: index, Sub_index: sub_index}) {
					if inject(packet) {
						r.counts.carried.Add(1)
					} else {
						r.counts.refused.Add(1)
					}
					continue
				}
				r.counts.direct.Add(1)
			}
		}
		if err := handle.Send(packet, &address); err != nil {
			r.counts.send_failed.Add(1)
			if !send_error_logged {
				send_error_logged = true
				r.logger.Warn("game router: a packet could not be sent back out", "error", err)
			}
		}
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
	if err := r.sender.Send(packet, &address); err != nil {
		r.counts.deliver_failed.Add(1)
		return false
	}
	r.counts.delivered.Add(1)
	return true
}

// report writes the packet counts every count_every while they move, and
// once more when the router stops.
func (r *Router) report(stop <-chan struct{}) {
	defer r.done.Done()
	ticker := time.NewTicker(count_every)
	defer ticker.Stop()
	var last [7]uint64
	write := func() {
		now := [7]uint64{
			r.counts.captured.Load(), r.counts.carried.Load(), r.counts.refused.Load(),
			r.counts.direct.Load(), r.counts.delivered.Load(), r.counts.deliver_failed.Load(),
			r.counts.send_failed.Load(),
		}
		if now == last {
			return
		}
		last = now
		r.logger.Info("game router: packets", "captured", now[0], "carried", now[1], "refused", now[2],
			"direct", now[3], "delivered", now[4], "deliver_failed", now[5], "send_failed", now[6])
	}
	for {
		select {
		case <-stop:
			write()
			return
		case <-ticker.C:
			write()
		}
	}
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
