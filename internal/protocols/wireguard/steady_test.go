package wireguard

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/tun"

	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/routing"
)

// dual_stack_config is full_tunnel_config numbered in both families.
func dual_stack_config() string {
	return strings.Replace(full_tunnel_config(), "Address = 10.0.0.2/32", "Address = 10.0.0.2/32, fd00:29::7/128", 1)
}

func config_of(text string) protocols.Connection_config {
	return protocols.Connection_config{Data: []byte(text)}
}

// datagram is an IPv4 UDP packet with no checksum to keep: the translation
// itself is tested in package nat.
func datagram(source, destination string) []byte {
	packet := make([]byte, 28)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:], 28)
	packet[8] = 64
	packet[9] = 17
	s, d := netip.MustParseAddr(source).As4(), netip.MustParseAddr(destination).As4()
	copy(packet[12:16], s[:])
	copy(packet[16:20], d[:])
	binary.BigEndian.PutUint16(packet[20:], 51000)
	binary.BigEndian.PutUint16(packet[22:], 53)
	binary.BigEndian.PutUint16(packet[24:], 8)
	return packet
}

// resting is the plan an adapter at rest carries: its steady addresses and
// its MTU, nothing else.
func resting(mtu int, addresses ...netip.Prefix) routing.Plan {
	return routing.Plan{Addresses: addresses, Mtu: mtu}
}

// Whatever the node numbered the machine, the adapter carries its steady
// address, and a tunnel to another node finds it already there.
func Test_the_adapter_carries_its_steady_address_whatever_the_node_gave(t *testing.T) {
	a, _, _, fn, _ := handing_adapter(t)
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(context.Background(), config_of(split_tunnel_config())); err != nil {
		t.Fatal(err)
	}
	for i, plan := range fn.plans {
		if !reflect.DeepEqual(plan.Addresses, []netip.Prefix{steady_ipv4}) {
			t.Fatalf("plan %d puts %v on the adapter; want only %v", i, plan.Addresses, steady_ipv4)
		}
	}
}

// The packets carry the address the node gave: what the machine sends from
// the steady address leaves from it, and what arrives for it is delivered
// to the steady address.
func Test_the_device_carries_the_node_address_on_packets(t *testing.T) {
	adapter_side := new_fake_tun()
	var device tun.Device
	a, err := New_adapter("Singularity-Test", func(name string, mtu int, wrap device_wrapper) (tunnel, error) {
		device = wrap(adapter_side)
		return &fake_tunnel{handle: 42}, nil
	}, &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51443")}, &fake_network{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(context.Background(), config_of(split_tunnel_config())); err != nil {
		t.Fatal(err)
	}

	adapter_side.reads <- datagram(steady_ipv4.Addr().String(), "10.29.8.1")
	bufs, sizes := [][]byte{make([]byte, 1500)}, []int{0}
	if n, err := device.Read(bufs, sizes, 0); n != 1 || err != nil {
		t.Fatalf("Read() = %d, %v", n, err)
	}
	if source := netip.AddrFrom4([4]byte(bufs[0][12:16])); source != netip.MustParseAddr("10.29.8.5") {
		t.Fatalf("a packet from the steady address leaves from %s; want the node's 10.29.8.5", source)
	}

	if _, err := device.Write([][]byte{datagram("10.29.8.1", "10.29.8.5")}, 0); err != nil {
		t.Fatal(err)
	}
	if destination := netip.AddrFrom4([4]byte(adapter_side.written[0][16:20])); destination != steady_ipv4.Addr() {
		t.Fatalf("a packet for the node's address reaches the adapter for %s; want %s", destination, steady_ipv4.Addr())
	}
}

// A disconnect leaves the adapter up: its session runs on, and of what the
// tunnel put on it only its steady address stays.
func Test_disconnect_leaves_the_adapter_up_at_rest(t *testing.T) {
	a, ft, _, fn, ended := handing_adapter(t)
	connected := fn.last_plan
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ft.close_calls != 1 || len(*ended) != 0 || fn.clear_calls != 0 {
		t.Fatalf("closed %d devices, ended %d sessions, cleared %d times; want the device closed and the adapter left up",
			ft.close_calls, len(*ended), fn.clear_calls)
	}
	if want := resting(connected.Mtu, steady_ipv4); !reflect.DeepEqual(fn.last_plan, want) {
		t.Fatalf("the adapter is left with %+v; want %+v", fn.last_plan, want)
	}
	if a.Status().Running {
		t.Fatal("Status().Running = true after Disconnect")
	}
}

// A disconnect asked for by something already giving up -- a request
// cancelled, the service stopping -- still brings the adapter to rest
// rather than down.
func Test_a_cancelled_disconnect_still_leaves_the_adapter_at_rest(t *testing.T) {
	a, ft, _, fn, ended := handing_adapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	at_rest(t, ft, fn, ended, 1)
}

// In games-only mode the splitter sits over the translation: what the
// machine itself sends through the adapter -- the health probe -- leaves
// from the node's address, and what comes back for it that is no game's
// reaches the adapter for the steady one.
func Test_games_only_traffic_through_the_adapter_carries_the_node_address(t *testing.T) {
	router := &fake_router{}
	adapter_side := new_fake_tun()
	defer adapter_side.Close()
	var device tun.Device
	a, err := New_adapter("Singularity-Test", func(name string, mtu int, wrap device_wrapper) (tunnel, error) {
		device = wrap(adapter_side)
		return &closing_tunnel{fake_tunnel: fake_tunnel{handle: 7}, router: router}, nil
	}, &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}, &fake_network{})
	if err != nil {
		t.Fatal(err)
	}
	a.Enable_games(router, probe)
	if err := a.Connect(context.Background(), games_config()); err != nil {
		t.Fatal(err)
	}

	adapter_side.reads <- datagram(steady_ipv4.Addr().String(), "1.1.1.1")
	bufs, sizes := [][]byte{make([]byte, 1500)}, []int{0}
	if n, err := device.Read(bufs, sizes, 0); n != 1 || err != nil {
		t.Fatalf("Read() = %d, %v", n, err)
	}
	if source := netip.AddrFrom4([4]byte(bufs[0][12:16])); source != netip.MustParseAddr("10.0.0.2") {
		t.Fatalf("the probe leaves from %s; want the node's 10.0.0.2", source)
	}
	if _, err := device.Write([][]byte{datagram("1.1.1.1", "10.0.0.2")}, 0); err != nil {
		t.Fatal(err)
	}
	if len(adapter_side.written) != 1 {
		t.Fatalf("%d packets reached the adapter; want the probe's answer", len(adapter_side.written))
	}
	if destination := netip.AddrFrom4([4]byte(adapter_side.written[0][16:20])); destination != steady_ipv4.Addr() {
		t.Fatalf("the probe's answer reaches the adapter for %s; want %s", destination, steady_ipv4.Addr())
	}
}

// An adapter whose tunnel's routes would not come off is taken down rather
// than left half configured: its session ends and Clear takes it all off,
// and both failures are reported.
func Test_an_adapter_that_cannot_come_to_rest_is_taken_down(t *testing.T) {
	a, _, _, fn, ended := handing_adapter(t)
	fn.apply_err = errors.New("route boom")
	fn.clear_err = errors.New("clear boom")
	err := a.Disconnect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "route boom") || !strings.Contains(err.Error(), "clear boom") {
		t.Fatalf("Disconnect() error = %v; want both failures", err)
	}
	if !reflect.DeepEqual(*ended, []int{1}) || fn.clear_calls != 1 {
		t.Fatalf("devices closed when each session ended = %v, cleared %d times; want [1] and one clear", *ended, fn.clear_calls)
	}
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatalf("second Disconnect() = %v, want nil", err)
	}
}

// A connect that fails once its device exists leaves the adapter at rest,
// as a disconnect does: the next attempt finds it as it was.
func Test_a_connect_that_fails_after_its_device_leaves_the_adapter_at_rest(t *testing.T) {
	ft := &fake_tunnel{handle: 42, up_err: errors.New("the device would not come up")}
	fn := &fake_network{}
	a := new_test_adapter(t, ft, &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}, fn)
	ended := 0
	a.end_session = func(string) { ended++ }
	if err := a.Connect(context.Background(), connect_config()); err == nil {
		t.Fatal("Connect() succeeded with a device that would not come up")
	}
	if ft.close_calls != 1 || ended != 0 || fn.clear_calls != 0 {
		t.Fatalf("closed %d devices, ended %d sessions, cleared %d times; want the device closed and the adapter left up", ft.close_calls, ended, fn.clear_calls)
	}
	if want := resting(fn.plans[0].Mtu, steady_ipv4); !reflect.DeepEqual(fn.last_plan, want) {
		t.Fatalf("the adapter is left with %+v; want %+v", fn.last_plan, want)
	}
}

// What an Apply that failed half way left on the adapter is not known, so
// the adapter is not left at rest but taken down.
func Test_a_connect_whose_plan_did_not_apply_takes_the_adapter_down(t *testing.T) {
	ft := &fake_tunnel{handle: 42}
	fn := &fake_network{apply_err: errors.New("route boom")}
	a := new_test_adapter(t, ft, &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}, fn)
	closes_at_end := []int{}
	a.end_session = func(string) { closes_at_end = append(closes_at_end, ft.close_calls) }
	if err := a.Connect(context.Background(), connect_config()); err == nil {
		t.Fatal("Connect() succeeded with a plan that did not apply")
	}
	if !reflect.DeepEqual(closes_at_end, []int{1}) || fn.clear_calls != 1 || fn.apply_calls != 1 {
		t.Fatalf("devices closed when each session ended = %v, cleared %d, applied %d times; want [1], one clear, no second apply",
			closes_at_end, fn.clear_calls, fn.apply_calls)
	}
}

// A family once on the adapter stays on it: a tunnel without IPv6 after one
// with it takes no address off.
func Test_a_family_once_on_the_adapter_stays(t *testing.T) {
	ft := &fake_tunnel{handle: 42}
	fn := &fake_network{}
	a := new_test_adapter(t, ft, &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}, fn)
	if err := a.Connect(context.Background(), config_of(dual_stack_config())); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(context.Background(), connect_config()); err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{steady_ipv4, steady_ipv6}
	for i, plan := range fn.plans {
		if !reflect.DeepEqual(plan.Addresses, want) {
			t.Fatalf("plan %d puts %v on the adapter; want %v", i, plan.Addresses, want)
		}
	}
}

func Test_the_steady_addresses_are_one_host_each_from_ranges_nobody_numbers_from(t *testing.T) {
	if !steady_ipv4.Addr().Is4() || steady_ipv4.Bits() != 32 || !steady_ipv6.Addr().Is6() || steady_ipv6.Bits() != 128 {
		t.Fatalf("steady addresses %v and %v; want one host address of each family", steady_ipv4, steady_ipv6)
	}
	// RFC 6598 shared address space and RFC 4193 unique local addresses:
	// numbered by no home network and by no AntiZapret node.
	if !netip.MustParsePrefix("100.64.0.0/10").Contains(steady_ipv4.Addr()) || !netip.MustParsePrefix("fc00::/7").Contains(steady_ipv6.Addr()) {
		t.Fatalf("steady addresses %v and %v are outside the ranges chosen for them", steady_ipv4, steady_ipv6)
	}
}
