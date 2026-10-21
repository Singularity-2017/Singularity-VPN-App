package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/ipc"
)

func ping_addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("netip.ParseAddr(%q): %v", s, err)
	}
	return a
}

// listed signs in and lets the application read a server list, which is the
// only thing that tells it what there is to measure.
func listed(t *testing.T, f *fixture, servers ...api.Server) {
	t.Helper()
	sign_in(t, f)
	f.control_plane.servers = servers
	if _, err := f.app.Servers(context.Background()); err != nil {
		t.Fatalf("Servers() error = %v", err)
	}
}

func Test_before_any_server_list_there_is_nothing_to_measure_and_that_is_not_an_error(t *testing.T) {
	f := new_fixture(t)

	got, err := f.app.Ping_servers(context.Background())
	if err != nil {
		t.Fatalf("Ping_servers() before Servers error = %v, want none: it needs no token", err)
	}
	if got.Pings == nil || len(got.Pings) != 0 {
		t.Fatalf("Ping_servers() before Servers = %#v, want an empty list, not null", got.Pings)
	}
	if asked := f.pinger.addresses_asked(); len(asked) != 0 {
		t.Fatalf("pinged %v before there was a server list", asked)
	}
}

func Test_only_the_servers_that_answered_are_in_the_result_and_by_node_id_alone(t *testing.T) {
	f := new_fixture(t)
	f.pinger.rtts = map[netip.Addr]time.Duration{
		ping_addr(t, "192.0.2.1"): 12500 * time.Microsecond,
		ping_addr(t, "192.0.2.3"): 400 * time.Microsecond,
		ping_addr(t, "192.0.2.9"): 0, // answers, but with no time on it: not a measurement
	}
	listed(t, f,
		api.Server{Node_id: "node-3", Host: "192.0.2.3", Available: true},
		api.Server{Node_id: "node-1", Host: "192.0.2.1", Available: true},
		api.Server{Node_id: "node-2", Host: "192.0.2.2", Available: true}, // fails
		api.Server{Node_id: "node-4", Available: true},                    // no host
		api.Server{Node_id: "node-5", Host: "192.0.2.9", Available: true}, // a zero round trip
	)

	got, err := f.app.Ping_servers(context.Background())
	if err != nil {
		t.Fatalf("Ping_servers() error = %v", err)
	}
	want := []ipc.Server_ping{{Node_id: "node-1", Rtt_ms: 12.5}, {Node_id: "node-3", Rtt_ms: 0.4}}
	if !reflect.DeepEqual(got.Pings, want) {
		t.Fatalf("Ping_servers() = %+v, want %+v: answered servers only, sorted, in milliseconds", got.Pings, want)
	}

	asked := f.pinger.addresses_asked()
	if len(asked) != 4 {
		t.Fatalf("pinged %v, want the four servers that have a host and not the one without", asked)
	}

	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "192.0.2") {
		t.Fatalf("the result carries a node address: %s", wire)
	}
}

func Test_a_name_is_resolved_to_ipv4_and_a_name_that_does_not_resolve_is_a_silent_server(t *testing.T) {
	f := new_fixture(t)
	f.pinger.rtts = map[netip.Addr]time.Duration{ping_addr(t, "198.51.100.7"): 20 * time.Millisecond}
	f.app.resolve_ip4 = func(ctx context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "de1.example.test":
			return []netip.Addr{ping_addr(t, "198.51.100.7"), ping_addr(t, "198.51.100.8")}, nil
		case "empty.example.test":
			return nil, nil
		}
		return nil, errors.New("no such host")
	}
	listed(t, f,
		api.Server{Node_id: "node-1", Host: "de1.example.test"},
		api.Server{Node_id: "node-2", Host: "gone.example.test"},
		api.Server{Node_id: "node-3", Host: "empty.example.test"},
	)

	got, err := f.app.Ping_servers(context.Background())
	if err != nil {
		t.Fatalf("Ping_servers() error = %v", err)
	}
	want := []ipc.Server_ping{{Node_id: "node-1", Rtt_ms: 20}}
	if !reflect.DeepEqual(got.Pings, want) {
		t.Fatalf("Ping_servers() = %+v, want %+v", got.Pings, want)
	}
	if asked := f.pinger.addresses_asked(); len(asked) != 1 || asked[0] != ping_addr(t, "198.51.100.7") {
		t.Fatalf("pinged %v, want only the first address the name resolved to", asked)
	}
}

func Test_a_newer_server_list_replaces_what_is_measured(t *testing.T) {
	f := new_fixture(t)
	f.pinger.rtts = map[netip.Addr]time.Duration{
		ping_addr(t, "192.0.2.1"): 10 * time.Millisecond,
		ping_addr(t, "192.0.2.2"): 20 * time.Millisecond,
		ping_addr(t, "192.0.2.8"): 30 * time.Millisecond,
	}
	listed(t, f,
		api.Server{Node_id: "node-1", Host: "192.0.2.1"},
		api.Server{Node_id: "node-2", Host: "192.0.2.2"},
	)
	// node-1 moves, node-2 leaves the list.
	f.control_plane.servers = []api.Server{{Node_id: "node-1", Host: "192.0.2.8"}}
	if _, err := f.app.Servers(context.Background()); err != nil {
		t.Fatalf("Servers() error = %v", err)
	}

	got, err := f.app.Ping_servers(context.Background())
	if err != nil {
		t.Fatalf("Ping_servers() error = %v", err)
	}
	want := []ipc.Server_ping{{Node_id: "node-1", Rtt_ms: 30}}
	if !reflect.DeepEqual(got.Pings, want) {
		t.Fatalf("Ping_servers() = %+v, want %+v", got.Pings, want)
	}
}

func Test_a_list_that_failed_to_load_leaves_the_servers_already_known_in_place(t *testing.T) {
	f := new_fixture(t)
	f.pinger.rtts = map[netip.Addr]time.Duration{ping_addr(t, "192.0.2.1"): 10 * time.Millisecond}
	listed(t, f, api.Server{Node_id: "node-1", Host: "192.0.2.1"})

	f.control_plane.servers_err = errors.New("control plane is down")
	if _, err := f.app.Servers(context.Background()); err == nil {
		t.Fatal("Servers() = nil error, want the failure")
	}

	got, err := f.app.Ping_servers(context.Background())
	if err != nil || len(got.Pings) != 1 {
		t.Fatalf("Ping_servers() = %+v, %v; want the known server still measured", got, err)
	}
}

// An echo that never comes back must not hold the window's once-a-second
// question for longer than ping_timeout, and must leave nothing in the
// result: a server that did not answer is not shown at all.
func Test_an_echo_that_never_returns_is_cut_off_and_leaves_the_server_out(t *testing.T) {
	f := new_fixture(t)
	f.pinger.wait = time.Hour
	listed(t, f, api.Server{Node_id: "node-1", Host: "192.0.2.1"})

	start := time.Now()
	got, err := f.app.Ping_servers(context.Background())
	if err != nil || len(got.Pings) != 0 {
		t.Fatalf("Ping_servers() = %+v, %v; want an empty result", got, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Ping_servers() took %s, want it cut off near %s", took, ping_timeout)
	}
}

func Test_a_caller_that_has_gone_is_not_answered(t *testing.T) {
	f := new_fixture(t)
	f.pinger.rtts = map[netip.Addr]time.Duration{ping_addr(t, "192.0.2.1"): 10 * time.Millisecond}
	listed(t, f, api.Server{Node_id: "node-1", Host: "192.0.2.1"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.app.Ping_servers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ping_servers() with a cancelled caller = %v, want context.Canceled", err)
	}
}

func Test_after_sign_out_the_last_account_s_servers_are_not_measured(t *testing.T) {
	f := new_fixture(t)
	f.pinger.rtts = map[netip.Addr]time.Duration{ping_addr(t, "192.0.2.1"): 20 * time.Millisecond}
	listed(t, f, api.Server{Node_id: "node-1", Host: "192.0.2.1", Available: true})

	if err := f.app.Sign_out(context.Background()); err != nil {
		t.Fatalf("Sign_out() error = %v", err)
	}
	got, err := f.app.Ping_servers(context.Background())
	if err != nil {
		t.Fatalf("Ping_servers() after sign-out error = %v", err)
	}
	if len(got.Pings) != 0 {
		t.Fatalf("Ping_servers() after sign-out = %+v, want nothing: those were the previous account's servers", got.Pings)
	}
}
