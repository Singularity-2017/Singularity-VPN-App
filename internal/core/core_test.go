package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/reach"
	"singularity-vpn-app/internal/recovery"
	"singularity-vpn-app/internal/security"
	"singularity-vpn-app/internal/state"
)

// --- fakes ---------------------------------------------------------------

// fake_control_plane scripts every call the application makes, and records
// what it was called with. It answers without a network, which is what lets
// the real connection.Manager run inside these tests -- the first time that
// state machine has been driven by anything but its own package's tests.
type fake_control_plane struct {
	mu sync.Mutex

	recommend_requests []api.Recommend_request
	servers            []api.Server
	servers_err        error
	catalog            api.Reach_catalog
	catalog_err        error
	catalog_calls      atomic.Int32

	login_token string
	login_err   error
	login_calls []string // usernames, never passwords

	device     api.Device
	device_err error
	device_for []string // the names device tokens were minted under

	recommendation api.Recommendation
	recommend_err  error

	profile     api.Profile
	profile_err error

	config     api.Vpn_config
	config_err error
}

func (f *fake_control_plane) Login(ctx context.Context, username, password string) (string, error) {
	f.mu.Lock()
	f.login_calls = append(f.login_calls, username)
	f.mu.Unlock()
	return f.login_token, f.login_err
}

func (f *fake_control_plane) Mint_device_token(ctx context.Context, session_token, name string) (api.Device, error) {
	f.mu.Lock()
	f.device_for = append(f.device_for, name)
	f.mu.Unlock()
	return f.device, f.device_err
}

func (f *fake_control_plane) Recommend(ctx context.Context, token string, request api.Recommend_request) (api.Recommendation, error) {
	f.mu.Lock()
	f.recommend_requests = append(f.recommend_requests, request)
	f.mu.Unlock()
	return f.recommendation, f.recommend_err
}

func (f *fake_control_plane) Servers(ctx context.Context, token string) ([]api.Server, error) {
	return f.servers, f.servers_err
}

func (f *fake_control_plane) Reach_targets(ctx context.Context, token string) (api.Reach_catalog, error) {
	f.catalog_calls.Add(1)
	return f.catalog, f.catalog_err
}

func (f *fake_control_plane) Create_profile(ctx context.Context, token, protocol, node_id string, duration_days int) (api.Profile, error) {
	return f.profile, f.profile_err
}

func (f *fake_control_plane) Fetch_config(ctx context.Context, token, client_id, protocol, node_id string) (api.Vpn_config, error) {
	return f.config, f.config_err
}

// fake_protocol is one tunnel adapter that does nothing but remember
// whether it was asked to connect and to disconnect.
type fake_protocol struct {
	mu sync.Mutex

	name             string
	connect_err      error
	disconnect_err   error
	connect_calls    int
	disconnect_calls int
	running          bool
	since            time.Time
}

func (p *fake_protocol) Name() string { return p.name }

func (p *fake_protocol) Connect(ctx context.Context, config protocols.Connection_config) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connect_calls++
	if p.connect_err != nil {
		return p.connect_err
	}
	p.running = true
	return nil
}

// Disconnect honours ctx. A real adapter does -- it removes routes and DNS
// through calls that take one -- and a fake that ignored it would make
// Test_stop_disconnects_even_when_the_caller_context_is_cancelled pass no
// matter what Stop did with the context it was handed.
func (p *fake_protocol) Disconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disconnect_calls++
	p.running = false
	return p.disconnect_err
}

func (p *fake_protocol) Status() protocols.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return protocols.Status{Running: p.running, Since: p.since}
}

func (p *fake_protocol) disconnects() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.disconnect_calls
}

// fake_prober answers every probe the same way. Nothing here is testing the
// prober; internal/platform does that.
type fake_prober struct {
	sample diagnostics.Sample
	err    error
	calls  atomic.Int32
}

func (p *fake_prober) Probe(ctx context.Context) (diagnostics.Sample, error) {
	p.calls.Add(1)
	return p.sample, p.err
}

// fake_reach_prober answers a probe with a scripted result and records what
// it was asked. If enter is set it announces each arrival, and if release is
// set it waits there until the test lets it go.
type fake_reach_prober struct {
	results []reach.Result
	err     error
	calls   atomic.Int32
	enter   chan struct{}
	release chan struct{}
}

func (p *fake_reach_prober) Probe(ctx context.Context, targets []reach.Target) ([]reach.Result, error) {
	p.calls.Add(1)
	if p.enter != nil {
		p.enter <- struct{}{}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.results, p.err
}

// memory_state_store is connection.State_store over a value in memory.
type memory_state_store struct {
	mu       sync.Mutex
	current  state.State
	load_err error
	save_err error
}

func (s *memory_state_store) Load() (state.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, s.load_err
}

func (s *memory_state_store) Save(new_state state.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.save_err != nil {
		return s.save_err
	}
	s.current = new_state
	return nil
}

func (s *memory_state_store) get() state.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// --- fixtures ------------------------------------------------------------

type fixture struct {
	app           *Application
	control_plane *fake_control_plane
	secrets       *security.Memory_store
	store         *memory_state_store
	protocol      *fake_protocol
	prober        *fake_prober
	reach_prober  *fake_reach_prober
	bus           *events.Bus
}

func new_fixture(t *testing.T) *fixture {
	t.Helper()

	control_plane := &fake_control_plane{
		login_token: "session-token",
		device:      api.Device{Device_id: "device-1", Device_token: "device-token", Expires_at: time.Now().Add(time.Hour)},
		// A real control plane always names a node and a protocol; a first
		// connect asks it before provisioning.
		recommendation: api.Recommendation{Node_id: "node-1", Protocol: "wireguard"},
	}
	protocol := &fake_protocol{name: "wireguard"}
	registry, err := protocols.New_registry(protocol)
	if err != nil {
		t.Fatalf("building the protocol registry: %v", err)
	}

	f := &fixture{
		control_plane: control_plane,
		secrets:       &security.Memory_store{},
		store:         &memory_state_store{},
		protocol:      protocol,
		prober:        &fake_prober{sample: diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Dns_ok: true, Rtt: 10 * time.Millisecond}},
		reach_prober:  &fake_reach_prober{},
		bus:           events.New_bus(16),
	}

	app, err := New_application(Dependencies{
		Control_plane: control_plane,
		Secrets:       f.secrets,
		Store:         f.store,
		Protocols:     registry,
		Prober:        f.prober,
		Reach_prober:  f.reach_prober,
		Bus:           f.bus,
		Device_name:   "test machine",
	})
	if err != nil {
		t.Fatalf("New_application: %v", err)
	}
	f.app = app
	t.Cleanup(func() { f.bus.Close() })
	return f
}

func base_dependencies() Dependencies {
	registry, _ := protocols.New_registry(&fake_protocol{name: "wireguard"})
	return Dependencies{
		Control_plane: &fake_control_plane{},
		Secrets:       &security.Memory_store{},
		Store:         &memory_state_store{},
		Protocols:     registry,
		Prober:        &fake_prober{},
		Reach_prober:  &fake_reach_prober{},
		Bus:           events.New_bus(1),
		Device_name:   "test machine",
	}
}

// --- tests ---------------------------------------------------------------

func Test_the_application_refuses_missing_dependencies(t *testing.T) {
	cases := []struct {
		field  string
		mutate func(*Dependencies)
	}{
		{"Control_plane", func(d *Dependencies) { d.Control_plane = nil }},
		{"Secrets", func(d *Dependencies) { d.Secrets = nil }},
		{"Store", func(d *Dependencies) { d.Store = nil }},
		{"Protocols", func(d *Dependencies) { d.Protocols = nil }},
		{"Prober", func(d *Dependencies) { d.Prober = nil }},
		{"Reach_prober", func(d *Dependencies) { d.Reach_prober = nil }},
		{"Bus", func(d *Dependencies) { d.Bus = nil }},
		{"Device_name", func(d *Dependencies) { d.Device_name = "" }},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			dependencies := base_dependencies()
			tc.mutate(&dependencies)

			_, err := New_application(dependencies)
			if err == nil {
				t.Fatalf("New_application without %s = nil error, want a refusal naming it", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("New_application without %s: error = %q, want it to name the field", tc.field, err)
			}
		})
	}
}

// Test_a_zero_config_still_gets_a_usable_recovery_policy guards the one
// place this package fills in a default that connection.Config cannot fill
// in for it: recovery.Policy is built here, from the same numbers, and a
// Max_switch_attempts of zero would refuse every switch a person's
// connection ever needed.
func Test_a_zero_config_still_gets_a_usable_recovery_policy(t *testing.T) {
	got := recovery_config_with_defaults(recovery.Config{})
	want := recovery.Default_config()
	if got != want {
		t.Fatalf("recovery_config_with_defaults(zero) = %+v, want the documented defaults %+v", got, want)
	}
}

func Test_stop_disconnects_a_running_tunnel(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)

	if err := f.app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v, want nil", err)
	}
	if got := f.protocol.disconnects(); got != 1 {
		t.Fatalf("the protocol was disconnected %d times during Stop, want 1", got)
	}
}

// Test_stop_disconnects_even_when_the_caller_context_is_cancelled is the
// one that protects a person's routing table. Stop is reached from a
// service shutdown, which is exactly the moment the surrounding context has
// just been cancelled; if that cancellation reached the teardown, the
// routes and the DNS would stay on the machine after the service was gone.
func Test_stop_disconnects_even_when_the_caller_context_is_cancelled(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := f.app.Stop(ctx); err != nil {
		t.Fatalf("Stop(cancelled ctx) = %v, want nil", err)
	}
	if got := f.protocol.disconnects(); got != 1 {
		t.Fatalf("the protocol was disconnected %d times during Stop with a cancelled context, want 1", got)
	}
}

func Test_a_connect_without_credentials_says_not_signed_in(t *testing.T) {
	f := new_fixture(t)

	err := f.app.Connect(context.Background(), connect_params_auto())
	if !errors.Is(err, ipc.Err_not_signed_in) {
		t.Fatalf("Connect() without credentials = %v, want ipc.Err_not_signed_in -- an interface shows a sign-in form for that and a wrong-password message for unauthorized", err)
	}
}

// Test_recheck_probes_a_live_connection_at_once is the resume path: the
// machine woke up, and the connection is verified now rather than at the
// next tick.
func Test_recheck_probes_a_live_connection_at_once(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)

	before := f.prober.calls.Load()
	f.app.Recheck(context.Background())
	if got := f.prober.calls.Load() - before; got != 1 {
		t.Fatalf("Recheck probed %d times, want exactly one immediate pass", got)
	}
}

func Test_recheck_leaves_a_disconnected_machine_alone(t *testing.T) {
	f := new_fixture(t)

	f.app.Recheck(context.Background())
	if got := f.prober.calls.Load(); got != 0 {
		t.Fatalf("Recheck probed %d times with nothing connected, want none", got)
	}
}

// --- helpers that keep the tests readable --------------------------------

func sign_in(t *testing.T, f *fixture) {
	t.Helper()
	if _, err := f.app.Sign_in(context.Background(), sign_in_params()); err != nil {
		t.Fatalf("Sign_in: %v", err)
	}
}

func connect(t *testing.T, f *fixture) {
	t.Helper()
	f.control_plane.profile = api.Profile{
		Client_id:  "client-1",
		Config:     "[Interface]\n",
		Node_id:    "node-1",
		Node_ip:    "203.0.113.10",
		Protocol:   "wireguard",
		Expires_at: time.Now().Add(24 * time.Hour),
	}
	if err := f.app.Connect(context.Background(), connect_params_auto()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if f.app.manager.State() != connection.State_connected {
		t.Fatalf("after Connect the state is %q, want %q", f.app.manager.State(), connection.State_connected)
	}
}
