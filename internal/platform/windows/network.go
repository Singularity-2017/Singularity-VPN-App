//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"singularity-vpn-app/internal/platform"
	"singularity-vpn-app/internal/routing"
)

// pinned_route is an endpoint host route Apply put on an adapter other than
// the tunnel's own, so that the tunnel does not have to carry its own
// transport. Clear must remove exactly these, and the handle it is
// given carries no other way to find out where they went, so this type
// exists purely to remember what Apply did.
type pinned_route struct {
	interface_luid winipcfg.LUID
	destination    netip.Prefix
	next_hop       netip.Addr
}

// network is the Windows implementation of platform.Network, over winipcfg.
type network struct {
	mu     sync.Mutex
	pinned map[platform.Tunnel_handle][]pinned_route
}

// New_network builds the Windows implementation of platform.Network.
func New_network() platform.Network {
	return &network{pinned: make(map[platform.Tunnel_handle][]pinned_route)}
}

// Apply sets the tunnel adapter's addresses, MTU, routes and DNS, and pins
// the peer endpoint's host route on whichever adapter already reached it,
// so the tunnel's own transport keeps leaving the machine the way it did
// before the tunnel existed.
func (n *network) Apply(ctx context.Context, handle platform.Tunnel_handle, plan routing.Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	luid := winipcfg.LUID(handle)

	if err := luid.SetIPAddresses(plan.Addresses); err != nil {
		return fmt.Errorf("platform/windows: setting addresses: %w", err)
	}

	for _, family := range families_of_prefixes(plan.Addresses) {
		row, err := luid.IPInterface(family)
		if err != nil {
			return fmt.Errorf("platform/windows: reading the interface to set its MTU: %w", err)
		}
		row.NLMTU = uint32(plan.Mtu)
		if err := row.Set(); err != nil {
			return fmt.Errorf("platform/windows: setting MTU: %w", err)
		}
	}

	if err := luid.SetRoutes(to_route_data(plan.Routes)); err != nil {
		return fmt.Errorf("platform/windows: setting routes: %w", err)
	}

	// The endpoint host route is pinned in every case the plan asks for one,
	// the full tunnel included -- see host_routes_to_pin for why skipping it
	// there would leave the tunnel carrying its own handshake. This runs
	// after SetRoutes on purpose: find_best_route excludes the tunnel's own
	// LUID, so it answers with the route the endpoint would have taken
	// before this adapter existed.
	var pinned []pinned_route
	for _, destination := range host_routes_to_pin(plan) {
		row, ok, err := find_best_route(destination.Addr(), luid, nil)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("platform/windows: no route to %s outside the tunnel, so its own transport has nowhere to go", destination)
		}
		if err := row.interface_luid.AddRoute(destination, row.next_hop, 0); err != nil {
			return fmt.Errorf("platform/windows: pinning endpoint host route %s: %w", destination, err)
		}
		pinned = append(pinned, pinned_route{
			interface_luid: row.interface_luid,
			destination:    destination,
			next_hop:       row.next_hop,
		})
	}

	for _, family := range families_of_addrs(plan.Dns) {
		if err := luid.SetDNS(family, plan.Dns, nil); err != nil {
			return fmt.Errorf("platform/windows: setting DNS: %w", err)
		}
	}

	if len(pinned) > 0 {
		n.mu.Lock()
		n.pinned[handle] = pinned
		n.mu.Unlock()
	}

	return nil
}

// Clear removes everything Apply may have added: the tunnel adapter's
// routes, addresses and DNS for every family, and any host route Apply
// pinned on another adapter. It must be safe to call on a handle Apply never
// touched and safe to call twice, because it is the teardown path and it
// runs when something has already gone wrong -- every step below continues
// past its own failure rather than stopping half way through a machine's
// network configuration, and the errors are joined rather than dropped.
func (n *network) Clear(ctx context.Context, handle platform.Tunnel_handle) error {
	luid := winipcfg.LUID(handle)
	var errs []error

	if err := flush_routes(luid); err != nil {
		errs = append(errs, fmt.Errorf("platform/windows: flushing routes: %w", err))
	}
	if err := luid.FlushIPAddresses(windows.AF_UNSPEC); err != nil {
		errs = append(errs, fmt.Errorf("platform/windows: flushing addresses: %w", err))
	}
	if err := luid.FlushDNS(windows.AF_INET); err != nil {
		errs = append(errs, fmt.Errorf("platform/windows: flushing IPv4 DNS: %w", err))
	}
	if err := luid.FlushDNS(windows.AF_INET6); err != nil {
		errs = append(errs, fmt.Errorf("platform/windows: flushing IPv6 DNS: %w", err))
	}

	n.mu.Lock()
	pinned := n.pinned[handle]
	delete(n.pinned, handle)
	n.mu.Unlock()

	for _, route := range pinned {
		if err := route.interface_luid.DeleteRoute(route.destination, route.next_hop); err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
			errs = append(errs, fmt.Errorf("platform/windows: removing pinned host route %s: %w", route.destination, err))
		}
	}

	return errors.Join(errs...)
}

// flush_routes deletes every route on luid. It is winipcfg's FlushRoutes
// except that a route already gone when its turn comes counts as removed:
// the table is a snapshot, and Windows deletes some routes itself when
// another goes -- the on-link and broadcast routes of an address, an
// interface's IPv6 link-local routes. "Element not found" there is the
// goal reached, not a failure to report to the person disconnecting.
func flush_routes(luid winipcfg.LUID) error {
	rows, err := winipcfg.GetIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		return err
	}
	var errs []error
	for i := range rows {
		if rows[i].InterfaceLUID != luid {
			continue
		}
		if err := rows[i].Delete(); err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Repin moves every host route Apply pinned for this handle onto the path
// its destination would take now, outside the tunnel. See repin_routes for
// the order of operations and the failure cases.
func (n *network) Repin(ctx context.Context, handle platform.Tunnel_handle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	pins := n.pinned[handle]
	if len(pins) == 0 {
		return nil
	}
	moved, err := repin_routes(pins, winipcfg.LUID(handle), system_route_ops{})
	n.pinned[handle] = moved
	return err
}
