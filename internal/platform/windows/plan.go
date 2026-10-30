//go:build windows

package windows

import (
	"net/netip"
	"slices"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"singularity-vpn-app/internal/routing"
)

// This file holds every decision Apply and Clear make, written as small
// pure functions over plain values so a test can see them directly. The
// syscall wrappers that call winipcfg are meant to have nothing left to get
// wrong once these are right.

// to_route_data converts the routes a plan wants on this tunnel adapter into
// the form winipcfg.LUID.SetRoutes wants. Only routes with Through_tunnel
// true belong on this adapter -- the ones that don't are the endpoint host
// route, which pin_host_routes (in routes.go) puts somewhere else entirely.
// The next hop for a route through the tunnel is the unspecified address of
// its own family: an on-link route on the tunnel adapter, which is what a
// point-to-point WireGuard interface needs.
func to_route_data(routes []routing.Route) []*winipcfg.RouteData {
	var out []*winipcfg.RouteData
	for _, route := range routes {
		if !route.Through_tunnel {
			continue
		}
		out = append(out, &winipcfg.RouteData{
			Destination: route.Destination,
			NextHop:     unspecified_address(route.Destination.Addr()),
			Metric:      0,
		})
	}
	return out
}

// unspecified_address returns 0.0.0.0 or :: matching addr's family.
func unspecified_address(addr netip.Addr) netip.Addr {
	if addr.Is4() {
		return netip.IPv4Unspecified()
	}
	return netip.IPv6Unspecified()
}

// host_routes_to_pin returns the destinations routing.Plan_for marked
// Through_tunnel: false -- today, always the peer endpoint's own host route.
// They never go on the tunnel adapter; Apply pins each one on whichever
// adapter already had the best route to it.
//
// A full tunnel needs this most, not least. Its two half routes, 0.0.0.0/1
// and 128.0.0.0/1, are more specific than the machine's default route --
// that is the whole point of using them -- but "more specific than the
// default route" includes the route to the peer's own endpoint. Without a
// host route pinned outside the tunnel, the tunnel's own transport packets
// match the tunnel's own route, and the handshake is posted into the tunnel
// that needs the handshake to exist. Nothing comes up at all.
//
// The official Windows client avoids this a different way: it installs
// 0.0.0.0/0 itself and binds its UDP socket to the physical interface with
// conn.BindSocketToInterface4, re-binding as the interface changes. This
// client does not bind the socket, so the host route is what keeps the
// transport out of the tunnel -- which means it is required every time
// routing.Plan_for emits one, and there is no case where skipping it is
// safe. Taking the Plan rather than its routes is deliberate: the full
// tunnel flag is right here, so nobody reading this has to go looking for
// whether it is the exception. It is not.
func host_routes_to_pin(plan routing.Plan) []netip.Prefix {
	var out []netip.Prefix
	for _, route := range plan.Routes {
		if !route.Through_tunnel {
			out = append(out, route.Destination)
		}
	}
	return out
}

// families_of_prefixes reports which address families addresses touches, v4
// before v6, each at most once. It drives which family gets its MTU set and
// which family's routes get flushed and rebuilt.
func families_of_prefixes(prefixes []netip.Prefix) []winipcfg.AddressFamily {
	has_v4, has_v6 := false, false
	for _, prefix := range prefixes {
		if prefix.Addr().Is4() {
			has_v4 = true
		} else {
			has_v6 = true
		}
	}
	return families_from(has_v4, has_v6)
}

// families_of_addrs is families_of_prefixes for a list of bare addresses,
// used for the DNS resolvers rather than the routed prefixes.
func families_of_addrs(addrs []netip.Addr) []winipcfg.AddressFamily {
	has_v4, has_v6 := false, false
	for _, addr := range addrs {
		if addr.Is4() {
			has_v4 = true
		} else {
			has_v6 = true
		}
	}
	return families_from(has_v4, has_v6)
}

func families_from(has_v4, has_v6 bool) []winipcfg.AddressFamily {
	var out []winipcfg.AddressFamily
	if has_v4 {
		out = append(out, winipcfg.AddressFamily(windows.AF_INET))
	}
	if has_v6 {
		out = append(out, winipcfg.AddressFamily(windows.AF_INET6))
	}
	return out
}

// route_row is the part of winipcfg.MibIPforwardRow2 pick_route needs, kept
// separate so pick_route can be tested without the syscall that produces
// the real rows.
type route_row struct {
	interface_luid winipcfg.LUID
	destination    netip.Prefix
	next_hop       netip.Addr
}

// pick_route finds the most specific route to destination among rows,
// ignoring the interfaces in exclude: the tunnel adapters' own LUIDs. This
// is called after a tunnel's own routes are already in the table, and a
// tunnel's route to destination is exactly the one the endpoint host route
// exists to beat, so it must not be the answer.
func pick_route(rows []route_row, destination netip.Addr, exclude ...winipcfg.LUID) (route_row, bool) {
	best := route_row{}
	found := false
	for _, row := range rows {
		if slices.Contains(exclude, row.interface_luid) {
			continue
		}
		if !row.destination.IsValid() || !row.destination.Contains(destination) {
			continue
		}
		if !found || row.destination.Bits() > best.destination.Bits() {
			best = row
			found = true
		}
	}
	return best, found
}

// pick_repin_route is pick_route for moving existing pins: it also leaves
// out every pinned row (same interface, same destination, same next hop).
// Without that, a new gateway on the same adapter would answer with the old
// /32, which is still in the table and wins by prefix length -- and a pin
// already moved would answer for the one it replaced.
func pick_repin_route(rows []route_row, destination netip.Addr, exclude winipcfg.LUID, pinned []route_row) (route_row, bool) {
	kept := make([]route_row, 0, len(rows))
	for _, row := range rows {
		if slices.Contains(pinned, row) {
			continue
		}
		kept = append(kept, row)
	}
	return pick_route(kept, destination, exclude)
}

// plan_change is what moving the tunnel adapter from the plan it carries to
// the next one changes on the machine; what the two share is left alone.
// Each change is a notification every program on the machine reacts to by
// reading its adapters again, and on Windows 11 26H2 the DNS Client answers
// those one at a time: a server switch that took everything off and put it
// back kept it on most cores for a minute or two (2026-10-29). From the
// empty plan -- a tunnel's first Apply -- everything is new.
type plan_change struct {
	// interfaces are the families whose interface row (MTU, address
	// detection) is set: one new to the adapter, or every one when the MTU
	// changed.
	interfaces       []winipcfg.AddressFamily
	add_addresses    []netip.Prefix
	remove_addresses []netip.Prefix
	// add_routes and remove_routes go through the tunnel.
	add_routes    []routing.Route
	remove_routes []routing.Route
	// pin are host routes kept outside the tunnel: the endpoint moved to.
	// Every pin the next plan does not want comes off in Apply (unpin) --
	// the endpoint left, and one an earlier Apply could not take off.
	pin []netip.Prefix
	// dns is true when the resolvers differ: they, and the policy pointing
	// every name at them, are set again.
	dns bool
}

// change_between is what Apply changes to move the adapter from from to to.
func change_between(from, to routing.Plan) plan_change {
	var change plan_change
	carried := families_of_prefixes(from.Addresses)
	for _, family := range families_of_prefixes(to.Addresses) {
		if to.Mtu != from.Mtu || !slices.Contains(carried, family) {
			change.interfaces = append(change.interfaces, family)
		}
	}
	change.add_addresses = missing_from(from.Addresses, to.Addresses)
	change.remove_addresses = missing_from(to.Addresses, from.Addresses)
	change.add_routes = missing_from(tunnel_routes(from), tunnel_routes(to))
	change.remove_routes = missing_from(tunnel_routes(to), tunnel_routes(from))
	change.pin = missing_from(host_routes_to_pin(from), host_routes_to_pin(to))
	change.dns = !slices.Equal(from.Dns, to.Dns)
	return change
}

// tunnel_routes is the plan's routes through the tunnel adapter.
func tunnel_routes(plan routing.Plan) []routing.Route {
	var out []routing.Route
	for _, route := range plan.Routes {
		if route.Through_tunnel {
			out = append(out, route)
		}
	}
	return out
}

// missing_from returns what want has and have does not, in want's order.
func missing_from[T comparable](have, want []T) []T {
	var out []T
	for _, item := range want {
		if !slices.Contains(have, item) {
			out = append(out, item)
		}
	}
	return out
}
