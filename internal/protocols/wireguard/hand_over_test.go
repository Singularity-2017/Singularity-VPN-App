package wireguard

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"singularity-vpn-app/internal/protocols"
)

// handing_adapter is a connected test adapter. closes_at_end holds, for
// each session it ended, how many devices had closed by then: a session
// must not end while a device still reads it.
func handing_adapter(t *testing.T) (a *Adapter, ft *fake_tunnel, fr *fake_resolver, fn *fake_network, closes_at_end *[]int) {
	t.Helper()
	ft = &fake_tunnel{handle: 42}
	fr = &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}
	fn = &fake_network{}
	a = new_test_adapter(t, ft, fr, fn)
	closes_at_end = &[]int{}
	a.end_session = func(string) { *closes_at_end = append(*closes_at_end, ft.close_calls) }
	if err := a.Connect(context.Background(), protocols.Connection_config{Data: []byte(full_tunnel_config())}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	return a, ft, fr, fn, closes_at_end
}

func connect_config() protocols.Connection_config {
	return protocols.Connection_config{Data: []byte(full_tunnel_config())}
}

func Test_the_adapter_carries_its_amneziawg_name_and_nothing_else(t *testing.T) {
	a, _, _, _, _ := handing_adapter(t)
	other := new_test_adapter(t, &fake_tunnel{}, &fake_resolver{}, &fake_network{})
	if !a.Carries(a) || !a.Carries(Amnezia{a}) || !(Amnezia{a}).Carries(a) {
		t.Fatal("WireGuard and AmneziaWG on one adapter do not carry each other")
	}
	if a.Carries(other) || a.Carries(Amnezia{other}) {
		t.Fatal("the adapter carries a protocol on another adapter")
	}
}

// A switch between protocols of one adapter keeps the adapter up and what
// is on it in place: the old device carries traffic until the new connect
// takes over, Apply runs again over the plan it left, and nothing is
// cleared.
func Test_a_tunnel_handed_over_carries_on_until_the_next_connect_takes_it_up(t *testing.T) {
	a, ft, _, fn, closes_at_end := handing_adapter(t)
	release := a.Hand_over(context.Background())
	if a.Status().Running || ft.close_calls != 0 {
		t.Fatalf("after the hand-over running = %v and %d devices closed; want not this connection, still carrying", a.Status().Running, ft.close_calls)
	}
	if err := (Amnezia{a}).Connect(context.Background(), connect_config()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if ft.close_calls != 1 || len(*closes_at_end) != 0 || fn.clear_calls != 0 || fn.apply_calls != 2 {
		t.Fatalf("closed %d devices, ended %d sessions, cleared %d times, applied %d times; want the old device closed and the plan applied over what it left",
			ft.close_calls, len(*closes_at_end), fn.clear_calls, fn.apply_calls)
	}
}

// A connect that fails before it takes the tunnel up -- here, its endpoint
// does not resolve -- leaves it carrying traffic; the release takes it
// down: device first, then the session, then the plan.
func Test_a_tunnel_handed_over_and_not_taken_up_goes_down_with_its_release(t *testing.T) {
	a, ft, fr, fn, closes_at_end := handing_adapter(t)
	release := a.Hand_over(context.Background())
	fr.err = errors.New("no such host")
	if err := a.Connect(context.Background(), connect_config()); err == nil {
		t.Fatal("Connect() succeeded with an endpoint that does not resolve")
	}
	if ft.close_calls != 0 {
		t.Fatal("the tunnel handed over stopped before a connect took it up")
	}
	if err := release(context.Background()); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatalf("second release() error = %v", err)
	}
	if !reflect.DeepEqual(*closes_at_end, []int{1}) || fn.clear_calls != 1 {
		t.Fatalf("devices closed when each session ended = %v, cleared %d times; want [1] and one clear", *closes_at_end, fn.clear_calls)
	}
}

func Test_a_connect_that_fails_over_a_tunnel_handed_over_takes_it_all_down(t *testing.T) {
	a, ft, _, fn, closes_at_end := handing_adapter(t)
	release := a.Hand_over(context.Background())
	ft.up_err = errors.New("the device would not come up")
	if err := a.Connect(context.Background(), connect_config()); err == nil {
		t.Fatal("Connect() succeeded with a device that would not come up")
	}
	if err := release(context.Background()); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	// The old device closed when taken up, the new one in the unwind.
	if !reflect.DeepEqual(*closes_at_end, []int{2}) || fn.clear_calls != 1 {
		t.Fatalf("devices closed when each session ended = %v, cleared %d times; want [2] and one clear", *closes_at_end, fn.clear_calls)
	}
}

// A disconnect while a tunnel waits to be taken up takes it down: nothing
// of it outlives the connection the person ended.
func Test_disconnect_takes_down_a_tunnel_handed_over_and_not_taken_up(t *testing.T) {
	a, _, _, fn, closes_at_end := handing_adapter(t)
	release := a.Hand_over(context.Background())
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if !reflect.DeepEqual(*closes_at_end, []int{1}) || fn.clear_calls != 1 {
		t.Fatalf("devices closed when each session ended = %v, cleared %d times; want [1] and one clear", *closes_at_end, fn.clear_calls)
	}
}

func Test_disconnect_closes_the_device_then_ends_the_adapter_session(t *testing.T) {
	a, _, _, fn, closes_at_end := handing_adapter(t)
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	if !reflect.DeepEqual(*closes_at_end, []int{1}) || fn.clear_calls != 1 {
		t.Fatalf("devices closed when each session ended = %v, cleared %d times; want [1] and one clear", *closes_at_end, fn.clear_calls)
	}
}

func Test_hand_over_with_nothing_connected_hands_over_nothing(t *testing.T) {
	ft := &fake_tunnel{handle: 42}
	fn := &fake_network{}
	a := new_test_adapter(t, ft, &fake_resolver{}, fn)
	ended := 0
	a.end_session = func(string) { ended++ }
	if err := a.Hand_over(context.Background())(context.Background()); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if ft.close_calls != 0 || ended != 0 || fn.clear_calls != 0 {
		t.Fatalf("closed %d devices, ended %d sessions, cleared %d times with nothing connected", ft.close_calls, ended, fn.clear_calls)
	}
}

// A boost turned off or on keeps the tunnel: the games' capture stops just
// before the old device closes, and starts again on the new one.
func Test_a_games_tunnel_handed_over_stops_capture_only_when_taken_up(t *testing.T) {
	router := &fake_router{}
	a, _ := games_adapter(t, router, &fake_network{})
	a.Enable_games(router, probe)
	a.end_session = func(string) { router.events = append(router.events, "end") }
	if err := a.Connect(context.Background(), games_config()); err != nil {
		t.Fatal(err)
	}
	release := a.Hand_over(context.Background())
	if !reflect.DeepEqual(router.events, []string{"start"}) {
		t.Fatalf("events after the hand-over = %v; want the capture still running", router.events)
	}
	if err := a.Connect(context.Background(), games_config()); err != nil {
		t.Fatal(err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start", "stop", "close", "start", "stop", "close", "end"}
	if !reflect.DeepEqual(router.events, want) {
		t.Fatalf("events = %v; want %v", router.events, want)
	}
}
