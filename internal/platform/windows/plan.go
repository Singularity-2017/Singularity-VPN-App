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
// ignoring exclude. exclude is the tunnel adapter's own LUID: this is called
// after the tunnel's own routes are already in the table, and the tunnel's
// route to destination is exactly the one the endpoint host route exists to
// beat, so it must not be the answer.
func pick_route(rows []route_row, destination netip.Addr, exclude winipcfg.LUID) (route_row, bool) {
	best := route_row{}
	found := false
	for _, row := range rows {
		if row.interface_luid == exclude {
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
