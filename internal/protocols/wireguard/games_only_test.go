package wireguard

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/tun"

	"singularity-vpn-app/internal/games"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/split"
)

type fake_router struct {
	start_err error
	started   []games.Game
	splitter  *split.Splitter
	inject    func([]byte) bool
	delivered []split.Origin
	events    []string
}

func (f *fake_router) Start(selected []games.Game, splitter *split.Splitter, inject func([]byte) bool) error {
	f.events = append(f.events, "start")
	f.started, f.splitter, f.inject = selected, splitter, inject
	return f.start_err
}

func (f *fake_router) Deliver(packet []byte, origin split.Origin) bool {
	f.delivered = append(f.delivered, origin)
	return true
}

func (f *fake_router) Stop() error          { f.events = append(f.events, "stop"); return nil }
func (f *fake_router) Remove_driver() error { f.events = append(f.events, "remove"); return nil }

// closing_tunnel records its close in the router's event list, so the order
// of stop and close can be checked.
type closing_tunnel struct {
	fake_tunnel
	router *fake_router
}

func (c *closing_tunnel) Close() error {
	c.router.events = append(c.router.events, "close")
	return nil
}

var pubg = games.Game{Id: "pubg", Name: "PUBG", Images: []string{"TslGame.exe"}}

// games_adapter wires a factory that applies the wrapper the way the real
// one does, over a fake tun device.
func games_adapter(t *testing.T, router *fake_router, fn *fake_network) (*Adapter, *closing_tunnel) {
	t.Helper()
	ct := &closing_tunnel{fake_tunnel: fake_tunnel{handle: 7}, router: router}
	a, err := New_adapter("Singularity-Test", func(name string, mtu int, wrap device_wrapper) (tunnel, error) {
		if wrap != nil {
			var device tun.Device = new_fake_tun()
			wrap(device)
		}
		return ct, nil
	}, &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}, fn)
	if err != nil {
		t.Fatal(err)
	}
	return a, ct
}

var probe = []netip.Prefix{netip.MustParsePrefix("1.1.1.1/32")}

func games_config() protocols.Connection_config {
	return protocols.Connection_config{Data: []byte(full_tunnel_config()), Split: protocols.Split{Games: []games.Game{pubg}}}
}

func Test_games_only_routes_only_the_probe_and_starts_the_router(t *testing.T) {
	router := &fake_router{}
	fn := &fake_network{}
	a, _ := games_adapter(t, router, fn)
	a.Enable_games(router, probe)
	if err := a.Connect(context.Background(), games_config()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(router.started, []games.Game{pubg}) || router.splitter == nil || router.inject == nil {
		t.Fatalf("router started with %+v", router.started)
	}
	if fn.last_plan.Full_tunnel || len(fn.last_plan.Dns) != 0 || len(fn.last_plan.Routes) != 1 || fn.last_plan.Routes[0].Destination != probe[0] {
		t.Fatalf("plan = %+v; want only the probe route and no DNS", fn.last_plan)
	}
}

func Test_games_only_stops_capture_before_the_tunnel_closes(t *testing.T) {
	router := &fake_router{}
	a, _ := games_adapter(t, router, &fake_network{})
	a.Enable_games(router, probe)
	if err := a.Connect(context.Background(), games_config()); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(router.events, []string{"start", "stop", "close"}) {
		t.Fatalf("events = %v; want start, stop, close", router.events)
	}
}

func Test_games_only_without_a_router_is_refused(t *testing.T) {
	a, ct := games_adapter(t, &fake_router{}, &fake_network{})
	if err := a.Connect(context.Background(), games_config()); err == nil {
		t.Fatal("games-only connected without a packet driver")
	}
	if ct.set_config_calls != 0 {
		t.Fatal("a tunnel was built before the refusal")
	}
}

func Test_a_router_that_cannot_start_leaves_nothing_up(t *testing.T) {
	router := &fake_router{start_err: errors.New("driver blocked")}
	fn := &fake_network{}
	a, _ := games_adapter(t, router, fn)
	a.Enable_games(router, probe)
	if err := a.Connect(context.Background(), games_config()); err == nil {
		t.Fatal("Connect hid the router failure")
	}
	if a.Status().Running || fn.clear_calls != 1 {
		t.Fatalf("running %v, clears %d; want the tunnel unwound", a.Status().Running, fn.clear_calls)
	}
}

func Test_a_touchy_game_removes_the_driver_and_carries_the_whole_machine(t *testing.T) {
	router := &fake_router{}
	fn := &fake_network{}
	a, _ := games_adapter(t, router, fn)
	a.Enable_games(router, probe)
	config := protocols.Connection_config{Data: []byte(full_tunnel_config()), Split: protocols.Split{Driver_forbidden: true}}
	if err := a.Connect(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(router.events, []string{"remove"}) || !fn.last_plan.Full_tunnel {
		t.Fatalf("events %v, full tunnel %v; want the driver removed and the whole machine", router.events, fn.last_plan.Full_tunnel)
	}
}
