//go:build windows

package windows

import (
	"net/netip"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"singularity-vpn-app/internal/routing"
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

// to_route_data must keep only the through-tunnel routes, translate each
// into a winipcfg.RouteData with the destination's own unspecified address
// as next hop, and drop nothing and reorder nothing.
func Test_to_route_data_keeps_only_through_tunnel_routes_in_order(t *testing.T) {
	routes := []routing.Route{
		{Destination: prefix(t, "0.0.0.0/1"), Through_tunnel: true},
		{Destination: prefix(t, "203.0.113.5/32"), Through_tunnel: false},
		{Destination: prefix(t, "128.0.0.0/1"), Through_tunnel: true},
	}

	got := to_route_data(routes)

	want := []*winipcfg.RouteData{
		{Destination: prefix(t, "0.0.0.0/1"), NextHop: netip.IPv4Unspecified(), Metric: 0},
		{Destination: prefix(t, "128.0.0.0/1"), NextHop: netip.IPv4Unspecified(), Metric: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("to_route_data() =\n%+v\nwant\n%+v", deref_all(got), deref_all(want))
	}
}

// An IPv6 through-tunnel route gets the IPv6 unspecified address as its next
// hop, not the IPv4 one.
func Test_to_route_data_uses_the_matching_family_unspecified_address(t *testing.T) {
	routes := []routing.Route{
		{Destination: prefix(t, "::/1"), Through_tunnel: true},
	}

	got := to_route_data(routes)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].NextHop != netip.IPv6Unspecified() {
		t.Fatalf("NextHop = %v, want %v", got[0].NextHop, netip.IPv6Unspecified())
	}
}

func Test_to_route_data_of_no_through_tunnel_routes_is_empty(t *testing.T) {
	routes := []routing.Route{
		{Destination: prefix(t, "203.0.113.5/32"), Through_tunnel: false},
	}
	got := to_route_data(routes)
	if len(got) != 0 {
		t.Fatalf("to_route_data() = %+v, want empty", deref_all(got))
	}
}

// host_routes_to_pin is the mirror of to_route_data: it keeps exactly the
// routes to_route_data drops.
func Test_host_routes_to_pin_keeps_only_the_excluded_routes(t *testing.T) {
	plan := routing.Plan{Routes: []routing.Route{
		{Destination: prefix(t, "0.0.0.0/1"), Through_tunnel: true},
		{Destination: prefix(t, "203.0.113.5/32"), Through_tunnel: false},
		{Destination: prefix(t, "128.0.0.0/1"), Through_tunnel: true},
	}}

	got := host_routes_to_pin(plan)
	want := []netip.Prefix{prefix(t, "203.0.113.5/32")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("host_routes_to_pin() = %+v, want %+v", got, want)
	}
}

func Test_host_routes_to_pin_of_all_through_tunnel_routes_is_empty(t *testing.T) {
	plan := routing.Plan{Routes: []routing.Route{
		{Destination: prefix(t, "0.0.0.0/1"), Through_tunnel: true},
	}}
	got := host_routes_to_pin(plan)
	if len(got) != 0 {
		t.Fatalf("host_routes_to_pin() = %+v, want empty", got)
	}
}

// The full tunnel is the case this is easiest to get wrong, and getting it
// wrong is total: its two half routes are more specific than the machine's
// default route, so without a host route pinned outside the tunnel the
// handshake to the peer is posted into the tunnel that needs the handshake.
// Nothing comes up at all, and it looks like a server problem. The plan
// routing.Plan_for builds is used here rather than a hand-written one, so
// this stays true of whatever that function actually emits.
func Test_a_full_tunnel_pins_its_endpoint_host_route_too(t *testing.T) {
	plan, err := routing.Plan_for(routing.Tunnel{
		Addresses: []netip.Prefix{prefix(t, "10.0.0.2/32")},
		Allowed:   []netip.Prefix{prefix(t, "0.0.0.0/0")},
		Dns:       []netip.Addr{netip.MustParseAddr("1.1.1.1")},
		Mtu:       1420,
		Endpoint:  netip.MustParseAddrPort("203.0.113.5:51080"),
	})
	if err != nil {
		t.Fatalf("Plan_for: %v", err)
	}
	if !plan.Full_tunnel {
		t.Fatalf("Plan_for did not call this a full tunnel")
	}

	got := host_routes_to_pin(plan)
	want := []netip.Prefix{prefix(t, "203.0.113.5/32")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("host_routes_to_pin() = %+v, want %+v -- a full tunnel that pins nothing carries its own transport", got, want)
	}
}

func Test_families_of_prefixes(t *testing.T) {
	cases := []struct {
		name string
		in   []netip.Prefix
		want []winipcfg.AddressFamily
	}{
		{"empty", nil, nil},
		{"v4 only", []netip.Prefix{prefix(t, "10.0.0.2/32")}, []winipcfg.AddressFamily{winipcfg.AddressFamily(windows.AF_INET)}},
		{"v6 only", []netip.Prefix{prefix(t, "fd00::2/128")}, []winipcfg.AddressFamily{winipcfg.AddressFamily(windows.AF_INET6)}},
		{
			"both, v4 first regardless of input order",
			[]netip.Prefix{prefix(t, "fd00::2/128"), prefix(t, "10.0.0.2/32")},
			[]winipcfg.AddressFamily{winipcfg.AddressFamily(windows.AF_INET), winipcfg.AddressFamily(windows.AF_INET6)},
		},
		{
			"duplicates of the same family collapse to one entry",
			[]netip.Prefix{prefix(t, "10.0.0.2/32"), prefix(t, "10.0.0.3/32")},
			[]winipcfg.AddressFamily{winipcfg.AddressFamily(windows.AF_INET)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := families_of_prefixes(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("families_of_prefixes(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func Test_families_of_addrs(t *testing.T) {
	cases := []struct {
		name string
		in   []netip.Addr
		want []winipcfg.AddressFamily
	}{
		{"empty", nil, nil},
		{"v4 only", []netip.Addr{addr(t, "1.1.1.1")}, []winipcfg.AddressFamily{winipcfg.AddressFamily(windows.AF_INET)}},
		{
			"both",
			[]netip.Addr{addr(t, "1.1.1.1"), addr(t, "2606:4700:4700::1111")},
			[]winipcfg.AddressFamily{winipcfg.AddressFamily(windows.AF_INET), winipcfg.AddressFamily(windows.AF_INET6)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := families_of_addrs(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("families_of_addrs(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// pick_route must ignore the excluded LUID -- the tunnel's own -- even when
// its route is the most specific match, and otherwise pick the longest
// matching prefix.
func Test_pick_route_ignores_the_excluded_luid_and_picks_longest_prefix(t *testing.T) {
	const tunnel_luid winipcfg.LUID = 1
	const physical_luid winipcfg.LUID = 2

	rows := []route_row{
		{interface_luid: physical_luid, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "192.168.1.1")},
		{interface_luid: tunnel_luid, destination: prefix(t, "203.0.113.0/24"), next_hop: netip.IPv4Unspecified()},
	}

	got, ok := pick_route(rows, addr(t, "203.0.113.5"), tunnel_luid)
	if !ok {
		t.Fatalf("pick_route: ok = false, want true")
	}
	want := route_row{interface_luid: physical_luid, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "192.168.1.1")}
	if got != want {
		t.Fatalf("pick_route() = %+v, want %+v", got, want)
	}
}

// Both tunnel adapters are kept for the life of the service, so an idle
// WireGuard one sits beside an OpenVPN tunnel carrying the whole machine on
// /1 routes: an echo to a server must leave by neither.
func Test_pick_route_ignores_every_tunnel_adapter_kept_beside_the_active_one(t *testing.T) {
	const idle_wireguard, physical, openvpn winipcfg.LUID = 1, 2, 3
	rows := []route_row{
		{interface_luid: physical, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "192.168.1.1")},
		{interface_luid: openvpn, destination: prefix(t, "0.0.0.0/1"), next_hop: netip.IPv4Unspecified()},
		{interface_luid: openvpn, destination: prefix(t, "128.0.0.0/1"), next_hop: netip.IPv4Unspecified()},
	}
	tunnels := pick_tunnel_luids([]interface_row{{luid: idle_wireguard, alias: "Singularity"}, {luid: physical, alias: "Ethernet"}, {luid: openvpn, alias: "Singularity OpenVPN"}}, []string{"Singularity", "Singularity OpenVPN"})
	got, ok := pick_route(rows, addr(t, "203.0.113.5"), tunnels...)
	if !ok || got.interface_luid != physical {
		t.Fatalf("pick_route() = %+v, %v with tunnels %v excluded; want the physical default route", got, ok, tunnels)
	}
}

func Test_pick_route_prefers_the_more_specific_of_two_eligible_rows(t *testing.T) {
	const other_luid winipcfg.LUID = 2
	const more_specific_luid winipcfg.LUID = 3

	rows := []route_row{
		{interface_luid: other_luid, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "192.168.1.1")},
		{interface_luid: more_specific_luid, destination: prefix(t, "203.0.113.0/24"), next_hop: addr(t, "10.0.0.1")},
	}

	got, ok := pick_route(rows, addr(t, "203.0.113.5"), winipcfg.LUID(99))
	if !ok {
		t.Fatalf("pick_route: ok = false, want true")
	}
	if got.interface_luid != more_specific_luid {
		t.Fatalf("pick_route() picked luid %v, want the more specific %v", got.interface_luid, more_specific_luid)
	}
}

func Test_pick_route_finds_nothing_when_no_row_contains_the_destination(t *testing.T) {
	rows := []route_row{
		{interface_luid: 1, destination: prefix(t, "10.0.0.0/24"), next_hop: addr(t, "10.0.0.1")},
	}
	_, ok := pick_route(rows, addr(t, "203.0.113.5"), winipcfg.LUID(99))
	if ok {
		t.Fatalf("pick_route: ok = true, want false")
	}
}

// deref_all is a test helper so a failure message shows RouteData values
// instead of pointers.
func deref_all(routes []*winipcfg.RouteData) []winipcfg.RouteData {
	out := make([]winipcfg.RouteData, len(routes))
	for i, r := range routes {
		out[i] = *r
	}
	return out
}

func Test_pick_repin_route_cases(t *testing.T) {
	const tunnel winipcfg.LUID = 1
	const wifi winipcfg.LUID = 2
	const eth winipcfg.LUID = 3
	stale := route_row{interface_luid: wifi, destination: prefix(t, "203.0.113.5/32"), next_hop: addr(t, "192.168.1.1")}
	tunnel_default := route_row{interface_luid: tunnel, destination: prefix(t, "0.0.0.0/0"), next_hop: netip.IPv4Unspecified()}
	wifi_old := route_row{interface_luid: wifi, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "192.168.1.1")}
	wifi_new := route_row{interface_luid: wifi, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "10.1.1.1")}
	eth_default := route_row{interface_luid: eth, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "172.16.0.1")}

	cases := []struct {
		name  string
		rows  []route_row
		want  route_row
		found bool
	}{
		{"unchanged gateway", []route_row{stale, tunnel_default, wifi_old}, wifi_old, true},
		{"new gateway same adapter", []route_row{stale, tunnel_default, wifi_new}, wifi_new, true},
		{"other adapter", []route_row{stale, tunnel_default, eth_default}, eth_default, true},
		{"only the stale pin left", []route_row{stale, tunnel_default}, route_row{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := pick_repin_route(c.rows, addr(t, "203.0.113.5"), tunnel, []route_row{stale})
			if ok != c.found || got != c.want {
				t.Fatalf("pick_repin_route() = %+v, %v; want %+v, %v", got, ok, c.want, c.found)
			}
		})
	}
}

// --- change_between ------------------------------------------------------

func antizapret_plan(t *testing.T, address, endpoint string) routing.Plan {
	t.Helper()
	return routing.Plan{
		Addresses: []netip.Prefix{prefix(t, address)},
		Routes: []routing.Route{
			{Destination: prefix(t, "10.224.0.0/15"), Through_tunnel: true},
			{Destination: prefix(t, "104.16.0.0/13"), Through_tunnel: true},
			{Destination: prefix(t, endpoint), Through_tunnel: false},
		},
		Dns: []netip.Addr{addr(t, "172.29.8.1")},
		Mtu: 1420,
	}
}

func Test_a_first_apply_sets_everything_the_plan_asks_for(t *testing.T) {
	plan := antizapret_plan(t, "172.29.8.2/32", "203.0.113.5/32")
	want := plan_change{
		interfaces:    []winipcfg.AddressFamily{windows.AF_INET},
		add_addresses: plan.Addresses,
		add_routes:    plan.Routes[:2],
		pin:           []netip.Prefix{prefix(t, "203.0.113.5/32")},
		dns:           true,
	}
	if got := change_between(routing.Plan{}, plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("change_between(nothing, plan) = %+v, want %+v", got, want)
	}
}

// Nodes that hand out the same address and resolver differ only in where
// the tunnel's own transport goes -- the endpoint left is let go in Apply
// (unpin). Anything more would be a network change every program on the
// machine reacts to.
func Test_moving_to_another_node_changes_only_the_endpoint_route(t *testing.T) {
	from := antizapret_plan(t, "172.29.8.2/32", "203.0.113.5/32")
	to := antizapret_plan(t, "172.29.8.2/32", "198.51.100.7/32")
	want := plan_change{
		pin: []netip.Prefix{prefix(t, "198.51.100.7/32")},
	}
	if got := change_between(from, to); !reflect.DeepEqual(got, want) {
		t.Fatalf("change_between() = %+v, want %+v", got, want)
	}
}

// A tunnel going leaves the adapter at rest: its routes and resolver come
// off, and its address and interface stay as they are -- no change Windows
// would send every program to read the adapters again for.
func Test_coming_to_rest_takes_off_routes_and_the_resolver_only(t *testing.T) {
	connected := antizapret_plan(t, "100.88.0.2/32", "203.0.113.5/32")
	resting := routing.Plan{Addresses: connected.Addresses, Mtu: connected.Mtu}
	want := plan_change{remove_routes: connected.Routes[:2], dns: true}
	if got := change_between(connected, resting); !reflect.DeepEqual(got, want) {
		t.Fatalf("change_between(connected, at rest) = %+v, want %+v", got, want)
	}
}

func Test_the_same_plan_again_changes_nothing(t *testing.T) {
	plan := antizapret_plan(t, "172.29.8.2/32", "203.0.113.5/32")
	if got := change_between(plan, plan); !reflect.DeepEqual(got, plan_change{}) {
		t.Fatalf("change_between(plan, plan) = %+v, want no change", got)
	}
}

func Test_only_the_address_routes_and_resolver_that_differ_change(t *testing.T) {
	from := antizapret_plan(t, "172.29.8.2/32", "203.0.113.5/32")
	to := antizapret_plan(t, "172.29.8.9/32", "203.0.113.5/32")
	telegram := routing.Route{Destination: prefix(t, "149.154.160.0/20"), Through_tunnel: true}
	to.Routes = append([]routing.Route{telegram}, to.Routes[1:]...)
	to.Dns = []netip.Addr{addr(t, "172.29.8.53")}
	want := plan_change{
		add_addresses:    []netip.Prefix{prefix(t, "172.29.8.9/32")},
		remove_addresses: []netip.Prefix{prefix(t, "172.29.8.2/32")},
		add_routes:       []routing.Route{telegram},
		remove_routes:    []routing.Route{from.Routes[0]},
		dns:              true,
	}
	if got := change_between(from, to); !reflect.DeepEqual(got, want) {
		t.Fatalf("change_between() = %+v, want %+v", got, want)
	}
}

func Test_a_new_mtu_sets_every_family_and_a_new_family_only_itself(t *testing.T) {
	from := antizapret_plan(t, "172.29.8.2/32", "203.0.113.5/32")
	to := antizapret_plan(t, "172.29.8.2/32", "203.0.113.5/32")
	to.Addresses = append(to.Addresses, prefix(t, "fd00::2/128"))
	if got := change_between(from, to).interfaces; !reflect.DeepEqual(got, []winipcfg.AddressFamily{windows.AF_INET6}) {
		t.Fatalf("interfaces set for a new family = %v, want IPv6 alone", got)
	}
	to.Mtu = 1280
	if got := change_between(from, to).interfaces; !reflect.DeepEqual(got, []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6}) {
		t.Fatalf("interfaces set for a new MTU = %v, want both families", got)
	}
}
