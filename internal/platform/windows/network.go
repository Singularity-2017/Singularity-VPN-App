//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
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

// dns_state records the adapter GUID and address families Apply configured
// DNS for, so Clear only flushes families that were actually set and can do
// so even after the tunnel device has already been closed.
type dns_state struct {
	guid     windows.GUID
	has_guid bool
	families []winipcfg.AddressFamily
}

// network is the Windows implementation of platform.Network, over winipcfg.
type network struct {
	mu     sync.Mutex
	pinned map[platform.Tunnel_handle][]pinned_route
	dns    map[platform.Tunnel_handle]dns_state
	// applied is the plan the last Apply on each handle left on its
	// adapter, for the next Apply to change only what differs.
	applied map[platform.Tunnel_handle]routing.Plan
}

// New_network builds the Windows implementation of platform.Network.
func New_network() platform.Network {
	return &network{
		pinned:  make(map[platform.Tunnel_handle][]pinned_route),
		dns:     make(map[platform.Tunnel_handle]dns_state),
		applied: make(map[platform.Tunnel_handle]routing.Plan),
	}
}

// Apply moves the tunnel adapter to plan from the plan the last Apply on
// this handle left there -- nothing, on a handle never applied or cleared
// since -- changing only what differs (see change_between): a tunnel handed
// over to the next on the same adapter keeps every address, route and
// resolver the two share, and Windows sees no change for them. A new peer
// endpoint's host route is pinned on whichever adapter already reached it,
// so the tunnel's own transport keeps leaving the machine the way it did
// before the tunnel existed.
func (n *network) Apply(ctx context.Context, handle platform.Tunnel_handle, plan routing.Plan) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	luid := winipcfg.LUID(handle)
	n.mu.Lock()
	carried := n.applied[handle]
	pinned := slices.Clone(n.pinned[handle])
	n.mu.Unlock()
	change := change_between(carried, plan)

	// Duplicate address detection is off, and set before the address is:
	// with it on (Windows' default is three probes) the new address stays
	// tentative for about three seconds, packets routed into the tunnel
	// meanwhile leave with the physical address as their source, and the
	// node decrypts them and drops them as not the peer's -- seen on the
	// owner's machine 2026-10-21 as every first verification failing,
	// the node's received count growing and its sent count flat. A
	// point-to-point tunnel has no neighbour to collide with; the official
	// WireGuard client turns it off the same way.
	for _, family := range change.interfaces {
		row, err := luid.IPInterface(family)
		if err != nil {
			return fmt.Errorf("platform/windows: reading the interface to set it up: %w", err)
		}
		row.NLMTU = uint32(plan.Mtu)
		row.DadTransmits = 0
		row.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
		if err := row.Set(); err != nil {
			return fmt.Errorf("platform/windows: setting MTU and address detection: %w", err)
		}
	}

	// What is new goes on before what is left comes off, so the adapter is
	// never without an address or a route both plans want.
	if err := add_addresses(luid, change.add_addresses); err != nil {
		return fmt.Errorf("platform/windows: setting addresses: %w", err)
	}

	if err := add_routes(luid, to_route_data(change.add_routes)); err != nil {
		return fmt.Errorf("platform/windows: setting routes: %w", err)
	}

	// The endpoint host route is pinned in every case the plan asks for one,
	// the full tunnel included -- see host_routes_to_pin for why skipping it
	// there would leave the tunnel carrying its own handshake. This runs
	// after add_routes on purpose: find_best_route excludes the tunnel's own
	// LUID, so it answers with the route the endpoint would have taken
	// before this adapter existed.
	// Recorded on every way out, not only on success: a pin added before a
	// later step failed is still in the table, and Clear can only take off
	// what it was told about.
	defer func() {
		n.mu.Lock()
		if len(pinned) > 0 {
			n.pinned[handle] = pinned
		} else {
			delete(n.pinned, handle)
		}
		n.mu.Unlock()
	}()
	for _, destination := range change.pin {
		row, ok, err := find_best_route(destination.Addr(), luid, nil)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("platform/windows: no route to %s outside the tunnel, so its own transport has nowhere to go", destination)
		}
		if err := add_host_route(row.interface_luid, destination, row.next_hop); err != nil {
			return fmt.Errorf("platform/windows: pinning endpoint host route %s: %w", destination, err)
		}
		pinned = append(pinned, pinned_route{
			interface_luid: row.interface_luid,
			destination:    destination,
			next_hop:       row.next_hop,
		})
	}

	if err := remove_routes(luid, change.remove_routes); err != nil {
		return fmt.Errorf("platform/windows: removing routes: %w", err)
	}
	if err := remove_addresses(luid, change.remove_addresses); err != nil {
		return fmt.Errorf("platform/windows: removing addresses: %w", err)
	}
	for _, destination := range change.unpin {
		pinned = unpin(pinned, destination, system_route_ops{})
	}

	switch {
	case change.dns:
		if err := n.set_dns(luid, handle, carried.Dns, plan.Dns); err != nil {
			return err
		}
	case len(change.pin) > 0 && len(plan.Dns) > 0:
		// Another node behind the same resolver address: what the last
		// node's resolver answered stays with it. AntiZapret answers a
		// blocked name with an address only the node that gave it maps.
		flush_resolver_cache()
	}

	n.mu.Lock()
	n.applied[handle] = plan
	n.mu.Unlock()
	return nil
}

// set_dns moves the adapter's resolvers from from to to: a family to no
// longer has is flushed, the others set, and the policy pointing every
// name at them written again -- or taken off with the last resolver.
func (n *network) set_dns(luid winipcfg.LUID, handle platform.Tunnel_handle, from, to []netip.Addr) error {
	families := families_of_addrs(to)
	state := dns_state{families: families}
	if guid, err := luid.GUID(); err == nil && guid != nil {
		state.guid = *guid
		state.has_guid = true
	}
	for _, family := range missing_from(families, families_of_addrs(from)) {
		if err := flush_interface_dns(luid, state, family); err != nil {
			return fmt.Errorf("platform/windows: flushing DNS: %w", err)
		}
	}
	n.mu.Lock()
	if len(families) > 0 {
		n.dns[handle] = state
	} else {
		delete(n.dns, handle)
	}
	n.mu.Unlock()
	for _, family := range families {
		if err := luid.SetDNS(family, to, nil); err != nil {
			return fmt.Errorf("platform/windows: setting DNS: %w", err)
		}
	}
	if len(to) == 0 {
		return clear_dns_policy(luid)
	}
	return set_dns_policy(luid, to)
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

	if err := clear_dns_policy(luid); err != nil {
		errs = append(errs, err)
	}

	n.mu.Lock()
	dns_cfg, had_dns := n.dns[handle]
	delete(n.dns, handle)
	pinned := n.pinned[handle]
	delete(n.pinned, handle)
	delete(n.applied, handle)
	n.mu.Unlock()

	if had_dns {
		for _, family := range dns_cfg.families {
			if err := flush_interface_dns(luid, dns_cfg, family); err != nil {
				errs = append(errs, fmt.Errorf("platform/windows: flushing DNS: %w", err))
			}
		}
	}

	// The tunnel adapters are kept for the life of the service (see
	// wireguard/kept_tun_windows.go), so the adapter is normally still here
	// and its routes and addresses come off one by one; one already gone
	// took them with it.
	if interface_exists(luid) {
		if err := flush_routes(luid); err != nil {
			errs = append(errs, fmt.Errorf("platform/windows: flushing routes: %w", err))
		}
		if err := luid.FlushIPAddresses(windows.AF_UNSPEC); err != nil {
			errs = append(errs, fmt.Errorf("platform/windows: flushing addresses: %w", err))
		}
	}

	for _, route := range pinned {
		if err := route.interface_luid.DeleteRoute(route.destination, route.next_hop); err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
			errs = append(errs, fmt.Errorf("platform/windows: removing pinned host route %s: %w", route.destination, err))
		}
	}

	return errors.Join(errs...)
}

// add_addresses adds unicast addresses without calling FlushIPAddresses
// across the machine's entire address table on a newly created adapter.
func add_addresses(luid winipcfg.LUID, addresses []netip.Prefix) error {
	for _, address := range addresses {
		if err := luid.AddIPAddress(address); err != nil && !errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
			return err
		}
	}
	return nil
}

// add_routes adds routes without calling FlushRoutes first: FlushRoutes on an
// adapter that just had its addresses set deletes the local/on-link/broadcast
// routes Windows created for those addresses and fires route-delete
// notifications before adding the plan's routes.
func add_routes(luid winipcfg.LUID, routes []*winipcfg.RouteData) error {
	for _, route := range routes {
		if err := luid.AddRoute(route.Destination, route.NextHop, route.Metric); err != nil && !errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
			return err
		}
	}
	return nil
}

// remove_addresses takes addresses off the adapter; one already gone
// counts as removed.
func remove_addresses(luid winipcfg.LUID, addresses []netip.Prefix) error {
	for _, address := range addresses {
		if err := luid.DeleteIPAddress(address); err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
			return err
		}
	}
	return nil
}

// remove_routes takes routes through the tunnel off the adapter; one
// already gone counts as removed.
func remove_routes(luid winipcfg.LUID, routes []routing.Route) error {
	for _, route := range to_route_data(routes) {
		if err := luid.DeleteRoute(route.Destination, route.NextHop); err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
			return err
		}
	}
	return nil
}

// unpin takes the host route pinned for destination off, and out of pins.
// One that would not come off stays in pins for Clear to try again: a host
// route to the node left is no reason to fail the tunnel that replaced it.
func unpin(pins []pinned_route, destination netip.Prefix, ops route_ops) []pinned_route {
	kept := make([]pinned_route, 0, len(pins))
	for _, pin := range pins {
		if pin.destination == destination && ops.delete_route(pin.interface_luid, pin.destination, pin.next_hop) == nil {
			continue
		}
		kept = append(kept, pin)
	}
	return kept
}

func interface_exists(luid winipcfg.LUID) bool {
	if luid == 0 {
		return false
	}
	_, err := luid.Interface()
	return err == nil
}

func flush_interface_dns(luid winipcfg.LUID, cfg dns_state, family winipcfg.AddressFamily) error {
	if cfg.has_guid {
		empty, err := windows.UTF16PtrFromString("")
		if err != nil {
			return err
		}
		settings := &winipcfg.DnsInterfaceSettings{
			Version:    winipcfg.DnsInterfaceSettingsVersion1,
			Flags:      winipcfg.DnsInterfaceSettingsFlagNameserver | winipcfg.DnsInterfaceSettingsFlagSearchList,
			NameServer: empty,
			SearchList: empty,
		}
		if family == windows.AF_INET6 {
			settings.Flags |= winipcfg.DnsInterfaceSettingsFlagIPv6
		}
		err = winipcfg.SetInterfaceDnsSettings(cfg.guid, settings)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_NOT_FOUND) {
			return nil
		}
		return err
	}
	err := luid.FlushDNS(family)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
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
