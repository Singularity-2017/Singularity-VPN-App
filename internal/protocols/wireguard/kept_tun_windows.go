//go:build windows

package wireguard

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// The tunnel's adapter is created once and kept for the life of the
// service; a tunnel only takes a session on it, started for it or left
// running by the tunnel handed over to it.
// Creating or removing a network adapter is a device installation: the
// Network Setup Service rebinds every protocol and filter, and every
// program on the machine reads its adapters again through the DNS Client
// service, which on Windows 11 26H2 answers those one at a time, a few
// dozen a second, every caller left waiting spinning a core. A server
// switch, a boost and its companion's retries each removed one adapter and
// created another, and kept that service on most cores for minutes, the
// whole machine stuttering with it (2026-10-29). The service removes them
// when it stops (Release_kept_adapters); one left by a process killed before
// it could is taken over by the next CreateAdapter of the same name and
// GUID, with no installation (seen 2026-10-29).
//
// A session outlives the tunnel that started it: handed over to the next
// tunnel on the adapter (Adapter.Hand_over), or left at rest when the
// tunnel goes (Adapter.rest), the adapter staying up with its steady
// addresses (steady.go). It ends with the adapters when the service stops,
// or when an adapter in a state nobody knows is taken down
// (kept_adapters.end).

// ring_capacity is the session's ring size, wireguard-go's: 8 MiB.
const ring_capacity = 0x800000

// wintun_adapter is what kept_adapters needs of a *wintun.Adapter, so a
// test can stand in for one.
type wintun_adapter interface {
	StartSession(capacity uint32) (wintun.Session, error)
	LUID() uint64
	Close() error
}

type kept_adapter struct {
	adapter wintun_adapter
	in_use  bool
	// session is the one running on the adapter while live, whether a
	// tunnel reads it or it waits for the next one.
	session wintun.Session
	live    bool
	// lost is set when the session ended under the tunnel reading it -- the
	// adapter disabled, the driver restarted. The adapter is kept up between
	// tunnels, so without it every later tunnel would be handed a session
	// that no longer carries anything.
	lost bool
}

// kept_adapters holds this process's adapters by name.
type kept_adapters struct {
	mu       sync.Mutex
	adapters map[string]*kept_adapter
	create   func(name string) (wintun_adapter, error)
	// clear takes off an adapter whatever is on it before a session starts
	// there -- the first in this process, or one after the last was lost:
	// a whole-machine route left under a games-only session, resolvers a
	// killed process left. New_windows_adapter makes it the network's Clear,
	// so the next Apply starts from nothing too.
	clear       func(adapter wintun_adapter)
	end_session func(session wintun.Session)
}

var kept = &kept_adapters{adapters: map[string]*kept_adapter{}, create: create_adapter, clear: clear_adapter, end_session: wintun.Session.End}

// session gives the adapter called name to a tunnel: the session a tunnel
// handed over left running, or a new one. The adapter is created only when
// this process has none by that name, or when the one it kept no longer
// starts sessions -- removed in Device Manager, or the driver updated under
// it.
func (k *kept_adapters) session(name string) (*kept_adapter, wintun.Session, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	held := k.adapters[name]
	if held != nil && held.in_use {
		return nil, wintun.Session{}, fmt.Errorf("wireguard: adapter %q already carries a tunnel", name)
	}
	if held != nil && held.live && !held.lost {
		held.in_use = true
		return held, held.session, nil
	}
	if held != nil {
		if held.live {
			// Lost under its tunnel: its handle is freed before another
			// session starts.
			k.end_session(held.session)
			held.live, held.lost = false, false
		}
		if err := k.start(held); err == nil {
			return held, held.session, nil
		}
		held.adapter.Close()
		delete(k.adapters, name)
	}
	adapter, err := k.create(name)
	if err != nil {
		return nil, wintun.Session{}, err
	}
	held = &kept_adapter{adapter: adapter}
	if err := k.start(held); err != nil {
		adapter.Close()
		return nil, wintun.Session{}, fmt.Errorf("wireguard: starting a session on the adapter: %w", err)
	}
	k.adapters[name] = held
	return held, held.session, nil
}

// start starts a session on an adapter that has none, cleared first of
// whatever the last tunnel's teardown left on it.
func (k *kept_adapters) start(held *kept_adapter) error {
	k.clear(held.adapter)
	session, err := held.adapter.StartSession(ring_capacity)
	if err != nil {
		return err
	}
	held.session, held.live, held.in_use = session, true, true
	return nil
}

// release leaves the adapter, its session still running, for the next
// tunnel.
func (k *kept_adapters) release(held *kept_adapter) {
	k.mu.Lock()
	held.in_use = false
	k.mu.Unlock()
}

// lose marks the session on held as ended under the tunnel reading it.
func (k *kept_adapters) lose(held *kept_adapter) {
	k.mu.Lock()
	held.lost = true
	k.mu.Unlock()
}

// end ends the session on the adapter called name once no tunnel reads
// it: the adapter goes down.
func (k *kept_adapters) end(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	held := k.adapters[name]
	if held == nil || held.in_use || !held.live {
		return
	}
	k.end_session(held.session)
	held.live, held.lost = false, false
}

// Release_kept_adapters removes the adapters no tunnel is using. The
// service calls it on its way out, once the tunnel is down, so a start
// straight after does not find one of the same identity still being
// removed.
func Release_kept_adapters() {
	kept.release_all()
}

func (k *kept_adapters) release_all() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for name, held := range k.adapters {
		if held.in_use {
			continue
		}
		if held.live {
			k.end_session(held.session)
		}
		held.adapter.Close()
		delete(k.adapters, name)
	}
}

// clear_adapter takes routes and addresses off the adapter. Each is a
// lookup in the route or address table when there is nothing to take off;
// resolvers are left to Apply, which sets them anyway.
func clear_adapter(adapter wintun_adapter) {
	luid := winipcfg.LUID(adapter.LUID())
	_ = luid.FlushRoutes(windows.AF_UNSPEC)
	_ = luid.FlushIPAddresses(windows.AF_UNSPEC)
}

// create_adapter creates the adapter, waiting out one of the same name
// still being removed: wintun answers "file not found" until it is gone --
// seen on the owner's machine 2026-10-21, when every rebuild removed the
// adapter and created it again straight away.
func create_adapter(name string) (wintun_adapter, error) {
	guid := adapter_guid(name)
	deadline := time.Now().Add(adapter_gone_wait)
	for {
		adapter, err := wintun.CreateAdapter(name, tun.WintunTunnelType, &guid)
		if err == nil {
			return adapter, nil
		}
		// A missing wintun.dll can carry the same code; it does not go
		// away by waiting.
		if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || strings.Contains(err.Error(), "wintun.dll") || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(adapter_gone_step)
	}
}

// kept_tun is tun.Device over one session on a kept adapter. It is
// wireguard-go's NativeTun, from amneziawg-go v1.0.4 tun/tun_windows.go --
//
//	SPDX-License-Identifier: MIT
//	Copyright (C) 2017-2023 WireGuard LLC. All Rights Reserved.
//
// -- except that Close leaves the session running for the next tunnel on
// the adapter (kept_adapters.end ends it), and that it does not spin
// waiting for packets above 800 Mbit/s.
type kept_tun struct {
	held       *kept_adapter
	name       string
	mtu        int
	session    wintun.Session
	read_wait  windows.Handle
	events     chan tun.Event
	running    sync.WaitGroup
	close_once sync.Once
	closed     atomic.Bool
}

func open_kept_tun(name string, mtu int) (*kept_tun, error) {
	held, session, err := kept.session(name)
	if err != nil {
		return nil, err
	}
	if mtu <= 0 {
		mtu = 1420
	}
	return &kept_tun{
		held:      held,
		name:      name,
		mtu:       mtu,
		session:   session,
		read_wait: session.ReadWaitEvent(),
		events:    make(chan tun.Event, 10),
	}, nil
}

func (t *kept_tun) File() *os.File { return nil }

func (t *kept_tun) Name() (string, error) { return t.name, nil }

func (t *kept_tun) MTU() (int, error) { return t.mtu, nil }

func (t *kept_tun) Events() <-chan tun.Event { return t.events }

func (t *kept_tun) BatchSize() int { return 1 }

// LUID is the adapter's, or 0 once closed.
func (t *kept_tun) LUID() uint64 {
	t.running.Add(1)
	defer t.running.Done()
	if t.closed.Load() {
		return 0
	}
	return t.held.adapter.LUID()
}

// Close stops reading and writing the session and leaves it, running, to
// the next tunnel on the adapter.
func (t *kept_tun) Close() error {
	t.close_once.Do(func() {
		t.closed.Store(true)
		windows.SetEvent(t.read_wait)
		t.running.Wait()
		kept.release(t.held)
		close(t.events)
	})
	return nil
}

// Read and Write, as NativeTun's, each assume one caller at a time.
func (t *kept_tun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	t.running.Add(1)
	defer t.running.Done()
	for {
		if t.closed.Load() {
			return 0, os.ErrClosed
		}
		packet, err := t.session.ReceivePacket()
		switch err {
		case nil:
			sizes[0] = copy(bufs[0][offset:], packet)
			t.session.ReleaseReceivePacket(packet)
			return 1, nil
		case windows.ERROR_NO_MORE_ITEMS:
			windows.WaitForSingleObject(t.read_wait, windows.INFINITE)
			continue
		case windows.ERROR_HANDLE_EOF:
			// The session ended under the tunnel: never by this process,
			// which ends one only when no tunnel reads it.
			kept.lose(t.held)
			return 0, os.ErrClosed
		case windows.ERROR_INVALID_DATA:
			kept.lose(t.held)
			return 0, errors.New("wireguard: the adapter's send ring is corrupt")
		}
		return 0, fmt.Errorf("wireguard: reading from the adapter: %w", err)
	}
}

func (t *kept_tun) Write(bufs [][]byte, offset int) (int, error) {
	t.running.Add(1)
	defer t.running.Done()
	if t.closed.Load() {
		return 0, os.ErrClosed
	}
	for i, buf := range bufs {
		packet, err := t.session.AllocateSendPacket(len(buf) - offset)
		switch err {
		case nil:
			copy(packet, buf[offset:])
			t.session.SendPacket(packet)
		case windows.ERROR_HANDLE_EOF:
			kept.lose(t.held)
			return i, os.ErrClosed
		case windows.ERROR_BUFFER_OVERFLOW:
			// The ring is full: the packet is dropped, as NativeTun drops it.
		default:
			return i, fmt.Errorf("wireguard: writing to the adapter: %w", err)
		}
	}
	return len(bufs), nil
}
