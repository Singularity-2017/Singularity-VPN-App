package routing

import (
	"errors"
	"fmt"
	"net/netip"
)

// Tunnel is what a protocol adapter says its tunnel needs, in terms no
// protocol is mentioned in.
type Tunnel struct {
	Addresses []netip.Prefix // what the tunnel adapter itself is numbered
	Allowed   []netip.Prefix // the destinations the tunnel claims
	Dns       []netip.Addr
	Mtu       int
	Endpoint  netip.AddrPort // where this tunnel's own packets go, resolved
	// Only, when not nil, is games-only mode: the machine routes just these
	// destinations into the tunnel instead of Allowed, and keeps its own
	// resolvers. The games' packets reach the tunnel without the routing
	// table; what is left here is what the service itself must send
	// through it, such as the target that proves the tunnel carries
	// traffic. The peer still accepts every destination in Allowed.
	Only []netip.Prefix
}

// Route is one entry the machine's routing table needs.
type Route struct {
	Destination    netip.Prefix
	Through_tunnel bool // false means "via the physical gateway"
}

// Plan is the operating-system state a tunnel needs: addresses to put on
// the adapter, routes to add, resolvers to use, and the MTU to set.
type Plan struct {
	Addresses   []netip.Prefix
	Routes      []Route
	Dns         []netip.Addr
	Mtu         int
	Full_tunnel bool
}

// Err_unroutable is what Plan_for returns when a Tunnel cannot be turned
// into a working Plan at all -- as opposed to a Tunnel that can, and whose
// Plan simply describes something narrower than "everything".
var Err_unroutable = errors.New("routing: tunnel is not routable")

var (
	ipv4_default = netip.MustParsePrefix("0.0.0.0/0")
	ipv6_default = netip.MustParsePrefix("::/0")

	ipv4_lower_half = netip.MustParsePrefix("0.0.0.0/1")
	ipv4_upper_half = netip.MustParsePrefix("128.0.0.0/1")
	ipv6_lower_half = netip.MustParsePrefix("::/1")
	ipv6_upper_half = netip.MustParsePrefix("8000::/1")
)

// Plan_for computes the machine state Tunnel needs. It never touches the
// system: it is the protocol-neutral description of what the system should
// look like, handed to an elevated layer that applies it.
func Plan_for(t Tunnel) (Plan, error) {
	if len(t.Addresses) == 0 {
		return Plan{}, fmt.Errorf("%w: tunnel has no addresses to put on its adapter", Err_unroutable)
	}
	if t.Mtu < 576 || t.Mtu > 65535 {
		return Plan{}, fmt.Errorf("%w: mtu %d is out of range 576-65535", Err_unroutable, t.Mtu)
	}
	// The endpoint is the one field whose absence would not announce itself.
	// An unresolved one builds a host route out of the zero address, which
	// netip quietly turns into an invalid prefix rather than an error, and
	// that invalid prefix travels on to the elevated layer that writes the
	// machine's routing table. Refusing here is the last place it is still
	// a value and not yet a system call.
	if !t.Endpoint.IsValid() {
		return Plan{}, fmt.Errorf("%w: tunnel endpoint was never resolved to an address", Err_unroutable)
	}

	claimed, dns := t.Allowed, t.Dns
	if t.Only != nil {
		claimed, dns = t.Only, nil
	}

	full_tunnel := false
	var routes []Route

	// A full tunnel does not get a default route: 0.0.0.0/0 is replaced by
	// 0.0.0.0/1 and 128.0.0.0/1 (::/0 by ::/1 and 8000::/1), both through
	// the tunnel. This looks like a trick and it is not -- both halves are
	// more specific than the machine's existing default route, so they win
	// without the existing one being touched. Nothing is removed and
	// nothing has to be restored: if this process dies at the wrong moment
	// (it is a VPN client, so it will), the machine is left with the
	// default route it always had and its internet still works. The
	// alternative -- replacing the default route and putting it back
	// afterwards -- fails by stranding a person offline with no obvious way
	// to find out why. This prefers the failure that leaves the machine
	// working.
	for _, allowed := range claimed {
		switch allowed {
		case ipv4_default:
			full_tunnel = true
			routes = append(routes,
				Route{Destination: ipv4_lower_half, Through_tunnel: true},
				Route{Destination: ipv4_upper_half, Through_tunnel: true},
			)
		case ipv6_default:
			full_tunnel = true
			routes = append(routes,
				Route{Destination: ipv6_lower_half, Through_tunnel: true},
				Route{Destination: ipv6_upper_half, Through_tunnel: true},
			)
		default:
			routes = append(routes, Route{Destination: allowed, Through_tunnel: true})
		}
	}

	// Every DNS resolver must be reachable. It is reachable if the tunnel is
	// full, or if it falls inside one of the allowed prefixes, or if it
	// falls outside all of them (resolved over the physical interface,
	// which is fine). The only case that is not fine is the empty one: a
	// full tunnel with no resolver at all sends every name lookup into a
	// tunnel with nothing behind it, and nothing resolves.
	if full_tunnel && len(dns) == 0 {
		return Plan{}, fmt.Errorf("%w: full tunnel has no DNS resolver, so nothing would resolve", Err_unroutable)
	}

	// The endpoint always gets its own host route, not through the tunnel.
	// Without it, the tunnel's own transport packets would match the
	// tunnel's own route and the tunnel would have to carry its own
	// transport, which does not work. This applies whenever the endpoint
	// falls inside an allowed prefix -- which for a full tunnel is always
	// (0.0.0.0/0 or ::/0 contains everything), and for a split tunnel is
	// exactly the subtle case the endpoint must be excluded from.
	if endpoint_needs_a_host_route(t.Endpoint, full_tunnel, claimed) {
		bits := 32
		if t.Endpoint.Addr().Is6() {
			bits = 128
		}
		host_route := netip.PrefixFrom(t.Endpoint.Addr(), bits)
		routes = append(routes, Route{Destination: host_route, Through_tunnel: false})
	}

	return Plan{
		Addresses:   t.Addresses,
		Routes:      routes,
		Dns:         dns,
		Mtu:         t.Mtu,
		Full_tunnel: full_tunnel,
	}, nil
}

// endpoint_needs_a_host_route reports whether the endpoint's own packets
// would otherwise be swallowed by one of the tunnel's own routes.
func endpoint_needs_a_host_route(endpoint netip.AddrPort, full_tunnel bool, allowed []netip.Prefix) bool {
	if full_tunnel {
		return true
	}
	addr := endpoint.Addr()
	for _, prefix := range allowed {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
