package wireguard

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"singularity-vpn-app/internal/platform"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/routing"
)

// --- fakes -----------------------------------------------------------

type fake_tunnel struct {
	handle platform.Tunnel_handle

	set_config_err error
	up_err         error
	close_err      error

	set_config_calls int
	up_calls         int
	close_calls      int
	last_uapi        string
}

func (f *fake_tunnel) Set_config(uapi string) error {
	f.set_config_calls++
	f.last_uapi = uapi
	return f.set_config_err
}

func (f *fake_tunnel) Up() error {
	f.up_calls++
	return f.up_err
}

func (f *fake_tunnel) Close() error {
	f.close_calls++
	return f.close_err
}

func (f *fake_tunnel) Handle() platform.Tunnel_handle {
	return f.handle
}

type fake_resolver struct {
	addr  netip.AddrPort
	err   error
	calls int
}

func (f *fake_resolver) Resolve(ctx context.Context, host_port string) (netip.AddrPort, error) {
	f.calls++
	return f.addr, f.err
}

type fake_network struct {
	apply_err error
	clear_err error

	apply_calls int
	clear_calls int
	repin_err   error
	repin_calls int
	last_handle platform.Tunnel_handle
	last_plan   routing.Plan
}

func (f *fake_network) Apply(ctx context.Context, handle platform.Tunnel_handle, plan routing.Plan) error {
	f.apply_calls++
	f.last_handle = handle
	f.last_plan = plan
	return f.apply_err
}

func (f *fake_network) Clear(ctx context.Context, handle platform.Tunnel_handle) error {
	f.clear_calls++
	f.last_handle = handle
	return f.clear_err
}

func (f *fake_network) Repin(ctx context.Context, handle platform.Tunnel_handle) error {
	f.repin_calls++
	f.last_handle = handle
	return f.repin_err
}

// --- test helpers ------------------------------------------------------

func new_test_adapter(t *testing.T, ft *fake_tunnel, fr *fake_resolver, fn *fake_network) *Adapter {
	t.Helper()
	a, err := New_adapter("Singularity-Test", func(name string, mtu int) (tunnel, error) {
		return ft, nil
	}, fr, fn)
	if err != nil {
		t.Fatalf("New_adapter: %v", err)
	}
	return a
}

// two_peer_config is a valid wg-quick text with two [Peer] sections -- Parse
// accepts it, but this adapter's routing plan does not, see
// Err_multiple_peers_unsupported.
func two_peer_config() string {
	return "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_full + "\n" +
		"Endpoint = one.example.org:51080\n" +
		"AllowedIPs = 10.0.0.0/24\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_second + "\n" +
		"Endpoint = two.example.org:51081\n" +
		"AllowedIPs = 10.0.1.0/24\n"
}

// --- Connect: the happy path -------------------------------------------

func Test_connect_success_brings_up_tunnel_and_status_reflects_it(t *testing.T) {
	ft := &fake_tunnel{handle: 42}
	fr := &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}
	fn := &fake_network{}
	a := new_test_adapter(t, ft, fr, fn)

	before := time.Now()
	err := a.Connect(context.Background(), protocols.Connection_config{Data: []byte(full_tunnel_config())})
	after := time.Now()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if ft.set_config_calls != 1 || ft.up_calls != 1 {
		t.Fatalf("set_config_calls=%d up_calls=%d, want 1 and 1", ft.set_config_calls, ft.up_calls)
	}
	if fn.apply_calls != 1 {
		t.Fatalf("apply_calls = %d, want 1", fn.apply_calls)
	}
	if fn.last_handle != platform.Tunnel_handle(42) {
		t.Fatalf("last_handle = %v, want 42", fn.last_handle)
	}
	if !fn.last_plan.Full_tunnel {
		t.Fatalf("plan.Full_tunnel = false, want true")
	}

	status := a.Status()
	if !status.Running {
		t.Fatalf("Status().Running = false, want true")
	}
	if status.Since.Before(before) || status.Since.After(after) {
		t.Fatalf("Status().Since = %v, want between %v and %v", status.Since, before, after)
	}
	if strings.Contains(status.Detail, private_key_full) {
		t.Fatalf("Status().Detail contains the private key")
	}
}

func Test_connect_when_already_connected_refuses_and_does_not_touch_the_running_tunnel(t *testing.T) {
	ft := &fake_tunnel{handle: 42}
	fr := &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}
	fn := &fake_network{}
	a := new_test_adapter(t, ft, fr, fn)

	if err := a.Connect(context.Background(), protocols.Connection_config{Data: []byte(full_tunnel_config())}); err != nil {
		t.Fatalf("first Connect: %v", err)
	}

	set_config_calls, up_calls, apply_calls := ft.set_config_calls, ft.up_calls, fn.apply_calls

	err := a.Connect(context.Background(), protocols.Connection_config{Data: []byte(full_tunnel_config())})
	if !errors.Is(err, Err_already_connected) {
		t.Fatalf("err = %v, want Err_already_connected", err)
	}
	if ft.set_config_calls != set_config_calls || ft.up_calls != up_calls || fn.apply_calls != apply_calls {
		t.Fatalf("second Connect touched the running tunnel")
	}
}

func Test_connect_parse_failure_returns_before_anything_exists(t *testing.T) {
	fr := &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}
	fn := &fake_network{}
	new_tunnel_calls := 0
	a, err := New_adapter("Singularity-Test", func(name string, mtu int) (tunnel, error) {
		new_tunnel_calls++
		return &fake_tunnel{}, nil
	}, fr, fn)
	if err != nil {
		t.Fatalf("New_adapter: %v", err)
	}

	err = a.Connect(context.Background(), protocols.Connection_config{Data: []byte("not a config")})
	if err == nil {
		t.Fatalf("Connect: want an error for unparsable config")
	}
	if new_tunnel_calls != 0 || fr.calls != 0 {
		t.Fatalf("parse failure touched the tunnel factory or the resolver")
	}
}

func Test_connect_resolve_failure_returns_before_anything_exists(t *testing.T) {
	new_tunnel_calls := 0
	fr := &fake_resolver{err: errors.New("boom")}
	fn := &fake_network{}
	a, err := New_adapter("Singularity-Test", func(name string, mtu int) (tunnel, error) {
		new_tunnel_calls++
		return &fake_tunnel{}, nil
	}, fr, fn)
	if err != nil {
		t.Fatalf("New_adapter: %v", err)
	}

	err = a.Connect(context.Background(), protocols.Connection_config{Data: []byte(full_tunnel_config())})
	if err == nil {
		t.Fatalf("Connect: want an error when resolve fails")
	}
	if new_tunnel_calls != 0 {
		t.Fatalf("resolve failure still created a tunnel device")
	}
}

func Test_connect_refuses_a_config_with_more_than_one_peer(t *testing.T) {
	new_tunnel_calls := 0
	fr := &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}
	fn := &fake_network{}
	a, err := New_adapter("Singularity-Test", func(name string, mtu int) (tunnel, error) {
		new_tunnel_calls++
		return &fake_tunnel{}, nil
	}, fr, fn)
	if err != nil {
		t.Fatalf("New_adapter: %v", err)
	}

	err = a.Connect(context.Background(), protocols.Connection_config{Data: []byte(two_peer_config())})
	if !errors.Is(err, Err_multiple_peers_unsupported) {
		t.Fatalf("err = %v, want Err_multiple_peers_unsupported", err)
	}
	if new_tunnel_calls != 0 || fr.calls != 0 {
		t.Fatalf("multiple-peer refusal touched the tunnel factory or the resolver")
	}
}

// --- Connect: the unwind table, the most valuable test in this package --

func Test_connect_failure_from_step_3_onward_unwinds(t *testing.T) {
	cases := []struct {
		name             string
		new_tunnel_err   error
		set_config_err   error
		apply_err        error
		up_err           error
		want_close_calls int
		want_clear_calls int
	}{
		{name: "tunnel creation fails", new_tunnel_err: errors.New("boom"), want_close_calls: 0, want_clear_calls: 0},
		{name: "set_config fails", set_config_err: errors.New("boom"), want_close_calls: 1, want_clear_calls: 1},
		{name: "apply fails", apply_err: errors.New("boom"), want_close_calls: 1, want_clear_calls: 1},
		{name: "up fails", up_err: errors.New("boom"), want_close_calls: 1, want_clear_calls: 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ft := &fake_tunnel{handle: 42, set_config_err: c.set_config_err, up_err: c.up_err}
			fr := &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}
			fn := &fake_network{apply_err: c.apply_err}

			a, err := New_adapter("Singularity-Test", func(name string, mtu int) (tunnel, error) {
				if c.new_tunnel_err != nil {
					return nil, c.new_tunnel_err
				}
				return ft, nil
			}, fr, fn)
			if err != nil {
				t.Fatalf("New_adapter: %v", err)
			}

			err = a.Connect(context.Background(), protocols.Connection_config{Data: []byte(full_tunnel_config())})
			if err == nil {
				t.Fatalf("Connect: want an error")
			}
			if ft.close_calls != c.want_close_calls {
				t.Fatalf("close_calls = %d, want %d", ft.close_calls, c.want_close_calls)
			}
			if fn.clear_calls != c.want_clear_calls {
				t.Fatalf("clear_calls = %d, want %d", fn.clear_calls, c.want_clear_calls)
			}
			if a.Status().Running {
				t.Fatalf("Status().Running = true after a failed Connect")
			}
		})
	}
}

func Test_connect_set_config_failure_does_not_leak_the_uapi_string(t *testing.T) {
	ft := &fake_tunnel{handle: 42, set_config_err: errors.New("device rejected configuration")}
	fr := &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}
	fn := &fake_network{}
	a := new_test_adapter(t, ft, fr, fn)

	err := a.Connect(context.Background(), protocols.Connection_config{Data: []byte(full_tunnel_config())})
	if err == nil {
		t.Fatalf("Connect: want an error")
	}
	if ft.last_uapi == "" {
		t.Fatalf("test bug: fake tunnel did not record the uapi string")
	}
	if strings.Contains(err.Error(), ft.last_uapi) {
		t.Fatalf("Connect error contains the full UAPI payload")
	}
	priv, parse_err := Parse_key(private_key_full)
	if parse_err != nil {
		t.Fatalf("Parse_key: %v", parse_err)
	}
	if strings.Contains(err.Error(), priv.Hex()) {
		t.Fatalf("Connect error contains the private key in hex")
	}
}

// --- Disconnect ----------------------------------------------------------

func Test_disconnect_when_not_connected_is_not_an_error_and_touches_nothing(t *testing.T) {
	fn := &fake_network{}
	a := new_test_adapter(t, &fake_tunnel{}, &fake_resolver{}, fn)

	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v, want nil", err)
	}
	if fn.clear_calls != 0 {
		t.Fatalf("Disconnect on an unconnected adapter called Clear")
	}
}

func Test_disconnect_clears_and_closes_even_when_clear_fails_and_joins_the_errors(t *testing.T) {
	ft := &fake_tunnel{handle: 42}
	fr := &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}
	fn := &fake_network{clear_err: errors.New("clear boom")}
	a := new_test_adapter(t, ft, fr, fn)

	if err := a.Connect(context.Background(), protocols.Connection_config{Data: []byte(full_tunnel_config())}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	err := a.Disconnect(context.Background())
	if err == nil {
		t.Fatalf("Disconnect: want an error when Clear fails")
	}
	if ft.close_calls != 1 {
		t.Fatalf("close_calls = %d, want 1 (Close attempted even though Clear failed)", ft.close_calls)
	}
	if a.Status().Running {
		t.Fatalf("Status().Running = true after Disconnect")
	}

	// A second Disconnect is idempotent regardless of the first's outcome.
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatalf("second Disconnect: %v, want nil", err)
	}
}

// --- the rest --------------------------------------------------------

func Test_name_returns_wireguard(t *testing.T) {
	a := new_test_adapter(t, &fake_tunnel{}, &fake_resolver{}, &fake_network{})
	if a.Name() != "wireguard" {
		t.Fatalf("Name() = %q, want %q", a.Name(), "wireguard")
	}
}

func Test_new_adapter_refuses_missing_dependencies(t *testing.T) {
	factory := func(string, int) (tunnel, error) { return nil, nil }
	if _, err := New_adapter("", factory, &fake_resolver{}, &fake_network{}); err == nil {
		t.Fatalf("New_adapter: want an error for an empty name")
	}
	if _, err := New_adapter("name", nil, &fake_resolver{}, &fake_network{}); err == nil {
		t.Fatalf("New_adapter: want an error for a nil tunnel factory")
	}
	if _, err := New_adapter("name", factory, nil, &fake_network{}); err == nil {
		t.Fatalf("New_adapter: want an error for a nil resolver")
	}
	if _, err := New_adapter("name", factory, &fake_resolver{}, nil); err == nil {
		t.Fatalf("New_adapter: want an error for a nil network")
	}
}

// A Since left over from a tunnel that is no longer up is a number that is
// not a measurement of anything: whoever reads Status next sees Running
// false beside a time the connection "has been up since". The project's
// rule is that a number is either a real measurement or it is absent.
func Test_a_disconnected_adapter_reports_no_time_it_has_been_up_since(t *testing.T) {
	adapter := new_test_adapter(t,
		&fake_tunnel{handle: 7},
		&fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")},
		&fake_network{})

	if err := adapter.Connect(context.Background(), protocols.Connection_config{
		Node_id: "node-1", Protocol: "wireguard", Data: []byte(full_tunnel_config()),
	}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := adapter.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	status := adapter.Status()
	if status.Running {
		t.Fatalf("Status().Running = true after Disconnect")
	}
	if !status.Since.IsZero() {
		t.Fatalf("Status().Since = %v after Disconnect, want the zero time", status.Since)
	}
}

// --- Repin -------------------------------------------------------------

func Test_repin_without_a_tunnel_does_nothing(t *testing.T) {
	fn := &fake_network{}
	a := new_test_adapter(t, &fake_tunnel{}, &fake_resolver{}, fn)

	if err := a.Repin(context.Background()); err != nil {
		t.Fatalf("Repin: %v, want nil", err)
	}
	if fn.repin_calls != 0 {
		t.Fatalf("Repin with no tunnel called the network")
	}
}

func Test_repin_with_a_tunnel_asks_the_network_for_its_handle(t *testing.T) {
	ft := &fake_tunnel{handle: 42}
	fr := &fake_resolver{addr: netip.MustParseAddrPort("203.0.113.5:51080")}
	fn := &fake_network{}
	a := new_test_adapter(t, ft, fr, fn)
	if err := a.Connect(context.Background(), protocols.Connection_config{Data: []byte(full_tunnel_config())}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := a.Repin(context.Background()); err != nil {
		t.Fatalf("Repin: %v", err)
	}
	if fn.repin_calls != 1 || fn.last_handle != 42 {
		t.Fatalf("repin_calls = %d, handle = %d; want 1 and 42", fn.repin_calls, fn.last_handle)
	}

	fn.repin_err = errors.New("no route")
	if err := a.Repin(context.Background()); err == nil {
		t.Fatalf("Repin: nil, want the network's error")
	}
}

var _ protocols.Repinner = (*Adapter)(nil)
