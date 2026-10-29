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
// service; a tunnel only starts a session on it and ends that session.
// Creating or removing a network adapter is a device installation: the
// Network Setup Service rebinds every protocol and filter, and every
// program on the machine reads its adapters again through the DNS Client
// service, which on Windows 11 26H2 answers those one at a time, a few
// dozen a second, every caller left waiting spinning a core. A server
// switch, a boost and its companion's retries each removed one adapter and
// created another, and kept that service on most cores for minutes, the
// whole machine stuttering with it (2026-10-29). Wintun removes the adapter
// itself when this process exits.

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
}

// kept_adapters holds this process's adapters by name.
type kept_adapters struct {
	mu       sync.Mutex
	adapters map[string]*kept_adapter
	create   func(name string) (wintun_adapter, error)
	// clear takes off a reused adapter whatever the last tunnel's teardown
	// left on it. With the adapter removed at every disconnect, a Clear that
	// failed half way could leave nothing behind; kept, it could leave a
	// whole-machine route under a games-only session.
	clear func(adapter wintun_adapter)
}

var kept = &kept_adapters{adapters: map[string]*kept_adapter{}, create: create_adapter, clear: clear_adapter}

// session starts a session on the adapter called name. The adapter is
// created only when this process has none by that name, or when the one it
// kept no longer starts sessions -- removed in Device Manager, or the
// driver updated under it.
func (k *kept_adapters) session(name string) (*kept_adapter, wintun.Session, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	held := k.adapters[name]
	if held != nil && held.in_use {
		return nil, wintun.Session{}, fmt.Errorf("wireguard: adapter %q already carries a tunnel", name)
	}
	if held != nil {
		k.clear(held.adapter)
		session, err := held.adapter.StartSession(ring_capacity)
		if err == nil {
			held.in_use = true
			return held, session, nil
		}
		held.adapter.Close()
		delete(k.adapters, name)
	}
	adapter, err := k.create(name)
	if err != nil {
		return nil, wintun.Session{}, err
	}
	session, err := adapter.StartSession(ring_capacity)
	if err != nil {
		adapter.Close()
		return nil, wintun.Session{}, fmt.Errorf("wireguard: starting a session on the adapter: %w", err)
	}
	held = &kept_adapter{adapter: adapter, in_use: true}
	k.adapters[name] = held
	return held, session, nil
}

// release leaves the adapter free for the next session.
func (k *kept_adapters) release(held *kept_adapter) {
	k.mu.Lock()
	held.in_use = false
	k.mu.Unlock()
}

// Release_kept_adapters removes the adapters no tunnel is using. The
// service calls it on its way out, once the tunnel is down, so a start
// straight after does not find one of the same identity still being
// removed.
func Release_kept_adapters() {
	kept.mu.Lock()
	defer kept.mu.Unlock()
	for name, held := range kept.adapters {
		if held.in_use {
			continue
		}
		held.adapter.Close()
		delete(kept.adapters, name)
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
// -- except that Close ends the session and leaves the adapter, and that it
// does not spin waiting for packets above 800 Mbit/s.
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

// LUID is the adapter's, or 0 once the session has ended.
func (t *kept_tun) LUID() uint64 {
	t.running.Add(1)
	defer t.running.Done()
	if t.closed.Load() {
		return 0
	}
	return t.held.adapter.LUID()
}

// Close ends the session and leaves the adapter for the next one.
func (t *kept_tun) Close() error {
	t.close_once.Do(func() {
		t.closed.Store(true)
		windows.SetEvent(t.read_wait)
		t.running.Wait()
		t.session.End()
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
			return 0, os.ErrClosed
		case windows.ERROR_INVALID_DATA:
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
			return i, os.ErrClosed
		case windows.ERROR_BUFFER_OVERFLOW:
			// The ring is full: the packet is dropped, as NativeTun drops it.
		default:
			return i, fmt.Errorf("wireguard: writing to the adapter: %w", err)
		}
	}
	return len(bufs), nil
}
