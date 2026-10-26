//go:build windows

package windows

import (
	"errors"
	"fmt"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// network_generation counts every route, interface and address notification while a
// watch runs, the ones on_change never hears about included; watching says
// a watch runs. Tables read at one generation stay true until the next.
var (
	network_generation atomic.Uint64
	watching           atomic.Int32
)

// Network_generation is how many route and interface changes the running
// watch has seen, and whether a watch runs at all: without one nothing says
// when a copy of the tables has gone stale.
func Network_generation() (uint64, bool) {
	return network_generation.Load(), watching.Load() > 0
}

// Watch_network calls on_change whenever a route or an interface changes on
// the machine, except for notifications about an interface ignore says to
// drop -- the tunnel's own adapter, whose routes and addresses this program
// changes itself and which say nothing about the network under it.
//
// The callbacks run on threads the operating system owns, and a notification
// arrives for every single route added or removed, so on_change must not
// block and the caller is expected to debounce. ignore runs there too.
//
// stop unregisters both callbacks and waits for any that is running.
func Watch_network(ignore func(winipcfg.LUID) bool, on_change func()) (stop func() error, err error) {
	notify := func(luid winipcfg.LUID) {
		if ignore != nil && ignore(luid) {
			return
		}
		on_change()
	}

	routes, err := winipcfg.RegisterRouteChangeCallback(func(_ winipcfg.MibNotificationType, route *winipcfg.MibIPforwardRow2) {
		network_generation.Add(1)
		// A single-address route is this program's own endpoint pin going
		// on or off the physical adapter -- connecting and disconnecting,
		// not the network changing. A real change also moves a wider route
		// (the default, the link's subnet) or the interface itself.
		if route.DestinationPrefix.Prefix().IsSingleIP() {
			return
		}
		notify(route.InterfaceLUID)
	})
	if err != nil {
		return nil, fmt.Errorf("platform/windows: watching route changes: %w", err)
	}
	interfaces, err := winipcfg.RegisterInterfaceChangeCallback(func(_ winipcfg.MibNotificationType, iface *winipcfg.MibIPInterfaceRow) {
		network_generation.Add(1)
		notify(iface.InterfaceLUID)
	})
	if err != nil {
		routes.Unregister()
		return nil, fmt.Errorf("platform/windows: watching interface changes: %w", err)
	}
	// An address finishing duplicate detection, or told to skip being a
	// source, moves no route; it only makes copies of the address table
	// stale. It is no network change.
	addresses, err := winipcfg.RegisterUnicastAddressChangeCallback(func(winipcfg.MibNotificationType, *winipcfg.MibUnicastIPAddressRow) {
		network_generation.Add(1)
	})
	if err != nil {
		routes.Unregister()
		interfaces.Unregister()
		return nil, fmt.Errorf("platform/windows: watching address changes: %w", err)
	}

	watching.Add(1)
	return func() error {
		err := errors.Join(routes.Unregister(), interfaces.Unregister(), addresses.Unregister())
		watching.Add(-1)
		return err
	}, nil
}
