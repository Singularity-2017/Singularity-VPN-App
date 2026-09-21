package routing

import (
	"errors"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func prefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("netip.ParsePrefix(%q): %v", s, err)
	}
	return p
}

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("netip.ParseAddr(%q): %v", s, err)
	}
	return a
}

func addr_port(t *testing.T, s string) netip.AddrPort {
	t.Helper()
	a, err := netip.ParseAddrPort(s)
	if err != nil {
		t.Fatalf("netip.ParseAddrPort(%q): %v", s, err)
	}
	return a
}

// The full-tunnel case: 0.0.0.0/0 is replaced by its two halves, the
// endpoint gets its own host route, and nothing else is added.
func Test_full_tunnel_plan_is_exactly_the_expected_plan(t *testing.T) {
	tunnel := Tunnel{
		Addresses: []netip.Prefix{prefix(t, "10.0.0.2/32")},
		Allowed:   []netip.Prefix{prefix(t, "0.0.0.0/0")},
		Dns:       []netip.Addr{addr(t, "1.1.1.1"), addr(t, "1.0.0.1")},
		Mtu:       1420,
		Endpoint:  addr_port(t, "203.0.113.5:51080"),
	}

	got, err := Plan_for(tunnel)
	if err != nil {
		t.Fatalf("Plan_for: %v", err)
	}

	want := Plan{
		Addresses: []netip.Prefix{prefix(t, "10.0.0.2/32")},
		Routes: []Route{
			{Destination: prefix(t, "0.0.0.0/1"), Through_tunnel: true},
			{Destination: prefix(t, "128.0.0.0/1"), Through_tunnel: true},
			{Destination: prefix(t, "203.0.113.5/32"), Through_tunnel: false},
		},
		Dns:         []netip.Addr{addr(t, "1.1.1.1"), addr(t, "1.0.0.1")},
		Mtu:         1420,
		Full_tunnel: true,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan_for() =\n%+v\nwant\n%+v", got, want)
	}
}

// 0.0.0.0/0 must never appear in the output routes: it is the invariant the
// whole full-tunnel design rests on.
func Test_full_tunnel_default_route_never_appears_in_output(t *testing.T) {
	tunnel := Tunnel{
		Addresses: []netip.Prefix{prefix(t, "10.0.0.2/32")},
		Allowed:   []netip.Prefix{prefix(t, "0.0.0.0/0")},
		Dns:       []netip.Addr{addr(t, "1.1.1.1")},
		Mtu:       1420,
		Endpoint:  addr_port(t, "203.0.113.5:51080"),
	}

	plan, err := Plan_for(tunnel)
	if err != nil {
		t.Fatalf("Plan_for: %v", err)
	}

	default_route := prefix(t, "0.0.0.0/0")
	for _, route := range plan.Routes {
		if route.Destination == default_route {
			t.Fatalf("plan.Routes contains the untouched default route: %+v", plan.Routes)
		}
	}
}

// A split tunnel produces one route per allowed prefix, in order, and no
// halves -- plus the endpoint host route, since 10.29.8.1 falls inside
// 10.29.8.0/24.
func Test_split_tunnel_plan_is_exactly_the_expected_plan(t *testing.T) {
	tunnel := Tunnel{
		Addresses: []netip.Prefix{prefix(t, "10.29.8.5/32")},
		Allowed: []netip.Prefix{
			prefix(t, "10.29.8.0/24"),
			prefix(t, "5.255.255.0/24"),
			prefix(t, "77.88.8.0/24"),
		},
		Dns:      []netip.Addr{addr(t, "10.29.8.1")},
		Mtu:      1420,
		Endpoint: addr_port(t, "10.29.8.1:51443"),
	}

	got, err := Plan_for(tunnel)
	if err != nil {
		t.Fatalf("Plan_for: %v", err)
	}

	want := Plan{
		Addresses: []netip.Prefix{prefix(t, "10.29.8.5/32")},
		Routes: []Route{
			{Destination: prefix(t, "10.29.8.0/24"), Through_tunnel: true},
			{Destination: prefix(t, "5.255.255.0/24"), Through_tunnel: true},
			{Destination: prefix(t, "77.88.8.0/24"), Through_tunnel: true},
			{Destination: prefix(t, "10.29.8.1/32"), Through_tunnel: false},
		},
		Dns:         []netip.Addr{addr(t, "10.29.8.1")},
		Mtu:         1420,
		Full_tunnel: false,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan_for() =\n%+v\nwant\n%+v", got, want)
	}
}

// The endpoint outside every allowed prefix, split tunnel, gets no host
// route: nothing needs excluding, because no allowed prefix would ever
// swallow the transport in the first place.
func Test_endpoint_outside_every_allowed_prefix_split_tunnel_gets_no_host_route(t *testing.T) {
	tunnel := Tunnel{
		Addresses: []netip.Prefix{prefix(t, "10.29.8.5/32")},
		Allowed:   []netip.Prefix{prefix(t, "10.29.8.0/24")},
		Dns:       []netip.Addr{addr(t, "8.8.8.8")},
		Mtu:       1420,
		Endpoint:  addr_port(t, "203.0.113.9:51443"),
	}

	plan, err := Plan_for(tunnel)
	if err != nil {
		t.Fatalf("Plan_for: %v", err)
	}

	want := []Route{
		{Destination: prefix(t, "10.29.8.0/24"), Through_tunnel: true},
	}
	if !reflect.DeepEqual(plan.Routes, want) {
		t.Fatalf("plan.Routes = %+v, want %+v (no host route for an endpoint outside every allowed prefix)", plan.Routes, want)
	}
}

func Test_a_full_tunnel_with_no_dns_is_refused(t *testing.T) {
	tunnel := Tunnel{
		Addresses: []netip.Prefix{prefix(t, "10.0.0.2/32")},
		Allowed:   []netip.Prefix{prefix(t, "0.0.0.0/0")},
		Mtu:       1420,
		Endpoint:  addr_port(t, "203.0.113.5:51080"),
	}

	_, err := Plan_for(tunnel)
	if !errors.Is(err, Err_unroutable) {
		t.Fatalf("err = %v, want Err_unroutable", err)
	}
}

func Test_a_tunnel_with_no_addresses_is_refused(t *testing.T) {
	tunnel := Tunnel{
		Allowed:  []netip.Prefix{prefix(t, "10.0.0.0/24")},
		Dns:      []netip.Addr{addr(t, "10.0.0.1")},
		Mtu:      1420,
		Endpoint: addr_port(t, "203.0.113.5:51080"),
	}

	_, err := Plan_for(tunnel)
	if !errors.Is(err, Err_unroutable) {
		t.Fatalf("err = %v, want Err_unroutable", err)
	}
}

func Test_mtu_out_of_range_is_refused_and_named_in_range_is_accepted(t *testing.T) {
	base := func(mtu int) Tunnel {
		return Tunnel{
			Addresses: []netip.Prefix{prefix(t, "10.0.0.2/32")},
			Allowed:   []netip.Prefix{prefix(t, "10.0.0.0/24")},
			Dns:       []netip.Addr{addr(t, "10.0.0.1")},
			Mtu:       mtu,
			Endpoint:  addr_port(t, "203.0.113.5:51080"),
		}
	}

	for _, mtu := range []int{68, 70000} {
		_, err := Plan_for(base(mtu))
		if !errors.Is(err, Err_unroutable) {
			t.Fatalf("mtu %d: err = %v, want Err_unroutable", mtu, err)
		}
		want := strconv.Itoa(mtu)
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("mtu %d: err = %q, does not name the number", mtu, err)
		}
	}

	for _, mtu := range []int{576, 1420, 65535} {
		if _, err := Plan_for(base(mtu)); err != nil {
			t.Fatalf("mtu %d: Plan_for: %v, want no error", mtu, err)
		}
	}
}

// IPv6: ::/0 becomes ::/1 and 8000::/1, and a v6 endpoint gets a /128.
func Test_ipv6_full_tunnel_uses_ipv6_halves_and_a_128_bit_host_route(t *testing.T) {
	tunnel := Tunnel{
		Addresses: []netip.Prefix{prefix(t, "fd00::2/128")},
		Allowed:   []netip.Prefix{prefix(t, "::/0")},
		Dns:       []netip.Addr{addr(t, "2606:4700:4700::1111")},
		Mtu:       1420,
		Endpoint:  addr_port(t, "[2001:db8::1]:51080"),
	}

	got, err := Plan_for(tunnel)
	if err != nil {
		t.Fatalf("Plan_for: %v", err)
	}

	want := Plan{
		Addresses: []netip.Prefix{prefix(t, "fd00::2/128")},
		Routes: []Route{
			{Destination: prefix(t, "::/1"), Through_tunnel: true},
			{Destination: prefix(t, "8000::/1"), Through_tunnel: true},
			{Destination: prefix(t, "2001:db8::1/128"), Through_tunnel: false},
		},
		Dns:         []netip.Addr{addr(t, "2606:4700:4700::1111")},
		Mtu:         1420,
		Full_tunnel: true,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan_for() =\n%+v\nwant\n%+v", got, want)
	}
}

// A Tunnel whose Endpoint was never resolved is the one missing value that
// does not announce itself: Plan_for used to carry it straight through into
// a host route built from an invalid address, and that route went on to the
// elevated layer that writes the machine's routing table.
func Test_a_tunnel_whose_endpoint_was_never_resolved_is_refused(t *testing.T) {
	tunnel := Tunnel{
		Addresses: []netip.Prefix{prefix(t, "10.0.0.2/32")},
		Allowed:   []netip.Prefix{prefix(t, "0.0.0.0/0")},
		Dns:       []netip.Addr{addr(t, "1.1.1.1")},
		Mtu:       1420,
	}

	plan, err := Plan_for(tunnel)
	if !errors.Is(err, Err_unroutable) {
		t.Fatalf("err = %v, want Err_unroutable", err)
	}
	for _, route := range plan.Routes {
		if !route.Destination.IsValid() {
			t.Fatalf("plan carries an invalid route destination: %v", route.Destination)
		}
	}
}
