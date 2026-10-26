package connection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/recovery"
	"singularity-vpn-app/internal/state"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var healthy_sample = diagnostics.Sample{
	Tunnel_up:   true,
	Internet_ok: true,
	Dns_ok:      true,
	Rtt:         20 * time.Millisecond,
	Taken_at:    epoch,
}

// --- fakes -------------------------------------------------------------

type fetch_config_response struct {
	config api.Vpn_config
	err    error
}

// fake_control_plane records every call it receives and answers from
// scripted results, so a test can assert exactly how many times each
// operation ran -- provisioning must run at most once per installation, and
// that is only checkable by counting.
type fake_control_plane struct {
	recommend_calls  int
	recommend_result api.Recommendation
	recommend_err    error

	recommend_requests []api.Recommend_request
	// recommend_hang, when set, makes Recommend announce itself on the
	// channel and then answer only when its context ends: a control
	// plane that does not respond.
	recommend_hang chan struct{}

	create_profile_calls int
	// asked_protocols is every protocol a profile or config was asked
	// for, in order.
	asked_protocols      []string
	asked_modes          []string
	create_profile_nodes []string

	create_profile_result api.Profile
	create_profile_err    error

	fetch_config_calls    int
	fetch_config_result   api.Vpn_config
	fetch_config_err      error
	fetch_config_sequence []fetch_config_response

	// profiles is the account's profiles as /me lists them; labels are
	// the names given, as "client_id=label".
	profiles      []api.Account_profile
	profiles_err  error
	fetch_clients []string
	labels        []string
}

func (f *fake_control_plane) Profiles(ctx context.Context, token string) ([]api.Account_profile, error) {
	return f.profiles, f.profiles_err
}

func (f *fake_control_plane) Label_profile(ctx context.Context, token, client_id, label string) (string, error) {
	f.labels = append(f.labels, client_id+"="+label)
	return label, nil
}

func (f *fake_control_plane) Recommend(ctx context.Context, token string, request api.Recommend_request) (api.Recommendation, error) {
	f.recommend_calls++
	f.recommend_requests = append(f.recommend_requests, request)
	if f.recommend_hang != nil {
		f.recommend_hang <- struct{}{}
		<-ctx.Done()
		return api.Recommendation{}, ctx.Err()
	}
	return f.recommend_result, f.recommend_err
}

func (f *fake_control_plane) Create_profile(ctx context.Context, token, protocol, node_id string, duration_days int) (api.Profile, error) {
	f.create_profile_calls++
	f.create_profile_nodes = append(f.create_profile_nodes, node_id)
	f.asked_protocols = append(f.asked_protocols, protocol)
	return f.create_profile_result, f.create_profile_err
}

func (f *fake_control_plane) Fetch_config(ctx context.Context, token, client_id, protocol, node_id, mode string) (api.Vpn_config, error) {
	index := f.fetch_config_calls
	f.fetch_config_calls++
	f.asked_protocols = append(f.asked_protocols, protocol)
	f.asked_modes = append(f.asked_modes, mode)
	f.fetch_clients = append(f.fetch_clients, client_id)
	if index < len(f.fetch_config_sequence) {
		response := f.fetch_config_sequence[index]
		return response.config, response.err
	}
	return f.fetch_config_result, f.fetch_config_err
}

// fake_token is the Token_source a test hands the manager; it never touches
// a real secure store.
type fake_token struct {
	token string
	err   error
}

func (f fake_token) Token(ctx context.Context) (string, error) {
	return f.token, f.err
}

// fake_protocol records every Connect and Disconnect it is asked to
// perform. connect_errs scripts a distinct error per call (by index),
// falling back to connect_err once the sequence is exhausted, so a test can
// make the first attempt fail and a retried one succeed.
type fake_protocol struct {
	name string

	connect_err  error
	connect_errs []error

	disconnect_err error

	connect_calls    int
	disconnect_calls int
	running          bool
}

func (f *fake_protocol) Name() string { return f.name }

func (f *fake_protocol) Connect(ctx context.Context, config protocols.Connection_config) error {
	index := f.connect_calls
	f.connect_calls++

	var err error
	if index < len(f.connect_errs) {
		err = f.connect_errs[index]
	} else {
		err = f.connect_err
	}
	if err != nil {
		return err
	}
	f.running = true
	return nil
}

func (f *fake_protocol) Disconnect(ctx context.Context) error {
	f.disconnect_calls++
	f.running = false
	return f.disconnect_err
}

func (f *fake_protocol) Status() protocols.Status {
	return protocols.Status{Running: f.running}
}

// fake_protocols is a protocols.Manager backed by a fixed set of adapters.
type fake_protocols struct {
	by_name map[string]*fake_protocol

	// wrap, when set, decides what Get hands out for an adapter, so a test
	// can give it optional interfaces such as protocols.Repinner.
	wrap func(*fake_protocol) protocols.Protocol
}

// repinning_protocol is a fake_protocol that is also a protocols.Repinner.
type repinning_protocol struct {
	*fake_protocol
	repin_calls int
	repin_err   error
}

func (r *repinning_protocol) Repin(ctx context.Context) error {
	r.repin_calls++
	return r.repin_err
}

func (f *fake_protocols) Get(name string) (protocols.Protocol, error) {
	adapter, ok := f.by_name[name]
	if !ok {
		return nil, protocols.Err_unknown_protocol
	}
	if f.wrap != nil {
		return f.wrap(adapter), nil
	}
	return adapter, nil
}

func (f *fake_protocols) Available() []protocols.Protocol {
	available := make([]protocols.Protocol, 0, len(f.by_name))
	for _, adapter := range f.by_name {
		available = append(available, adapter)
	}
	return available
}

type prober_response struct {
	sample diagnostics.Sample
	err    error
}

// fake_prober returns a scripted sequence of samples or errors, one per
// call; once the sequence is exhausted, it keeps returning the last entry,
// so a test only has to script as many probes as it actually cares about.
type fake_prober struct {
	responses []prober_response
	calls     int
}

func (f *fake_prober) Probe(ctx context.Context) (diagnostics.Sample, error) {
	index := f.calls
	if index >= len(f.responses) {
		index = len(f.responses) - 1
	}
	f.calls++
	response := f.responses[index]
	return response.sample, response.err
}

// set replaces the scripted responses and resets the call count, so a test
// can script a fresh sequence starting at index 0 regardless of how many
// probes an earlier Connect already consumed (Connect verifies through the
// same Prober).
func (f *fake_prober) set(responses []prober_response) {
	f.responses = responses
	f.calls = 0
}

// fake_store is an in-memory State_store, standing in for stage 1's real
// file-backed store.
type fake_store struct {
	state state.State

	load_err error
	save_err error

	save_calls int
}

func (f *fake_store) Load() (state.State, error) {
	if f.load_err != nil {
		return state.State{}, f.load_err
	}
	return f.state, nil
}

func (f *fake_store) Save(s state.State) error {
	f.save_calls++
	if f.save_err != nil {
		return f.save_err
	}
	f.state = s
	return nil
}

// manual_clock is the Now dependency: a test advances it explicitly, and
// nothing in this package ever reads a real clock during a test.
type manual_clock struct {
	now time.Time
}

func (c *manual_clock) Now() time.Time { return c.now }

func (c *manual_clock) advance(d time.Duration) time.Time {
	c.now = c.now.Add(d)
	return c.now
}

// id_sequence is the New_session_id dependency: deterministic, distinct ids
// in call order.
type id_sequence struct {
	prefix string
	n      int
}

func (s *id_sequence) next() string {
	s.n++
	return fmt.Sprintf("%s-%d", s.prefix, s.n)
}

// --- test harness --------------------------------------------------------

type test_env struct {
	t *testing.T

	control_plane *fake_control_plane
	token         fake_token
	protocol      *fake_protocol
	protocols     *fake_protocols
	prober        *fake_prober
	store         *fake_store
	policy        *recovery.Policy
	bus           *events.Bus
	clock         *manual_clock
	ids           *id_sequence
	config        Config
}

// new_test_env wires one node, "de-01" on "wireguard", reachable either
// through a fresh Create_profile or, once a client_id is set on the store,
// through Recommend and Fetch_config. Each test overrides only what it
// needs to exercise.
func new_test_env(t *testing.T) *test_env {
	protocol := &fake_protocol{name: "wireguard"}
	return &test_env{
		t: t,
		control_plane: &fake_control_plane{
			create_profile_result: api.Profile{
				Client_id:  "client-1",
				Config:     "config-bytes",
				Node_id:    "de-01",
				Node_ip:    "10.0.0.1",
				Protocol:   "wireguard",
				Expires_at: epoch.Add(30 * 24 * time.Hour),
			},
			fetch_config_result: api.Vpn_config{
				Config:     "config-bytes",
				Protocol:   "wireguard",
				Mode:       "full",
				Expires_at: epoch.Add(24 * time.Hour),
			},
			recommend_result: api.Recommendation{
				Node_id:  "de-01",
				Protocol: "wireguard",
			},
		},
		token:     fake_token{token: "token-1"},
		protocol:  protocol,
		protocols: &fake_protocols{by_name: map[string]*fake_protocol{"wireguard": protocol}},
		prober:    &fake_prober{responses: []prober_response{{sample: healthy_sample}}},
		store:     &fake_store{},
		policy:    recovery.New_policy(recovery.Default_config()),
		bus:       events.New_bus(0),
		clock:     &manual_clock{now: epoch},
		ids:       &id_sequence{prefix: "session"},
		config:    Default_config(),
	}
}

func (e *test_env) build() *Manager {
	manager, err := New_manager(Dependencies{
		Control_plane:  e.control_plane,
		Token:          e.token,
		Protocols:      e.protocols,
		Prober:         e.prober,
		Policy:         e.policy,
		Store:          e.store,
		Bus:            e.bus,
		Now:            e.clock.Now,
		New_session_id: e.ids.next,
		Config:         e.config,
	})
	if err != nil {
		e.t.Fatalf("New_manager() error = %v", err)
	}
	return manager
}

func drain_events(ch <-chan events.Event) []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func kinds_of(list []events.Event) []events.Kind {
	kinds := make([]events.Kind, len(list))
	for i, ev := range list {
		kinds[i] = ev.Kind
	}
	return kinds
}

func contains_kind(list []events.Event, kind events.Kind) bool {
	for _, ev := range list {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func kind_list_contains(list []events.Kind, kind events.Kind) bool {
	for _, k := range list {
		if k == kind {
			return true
		}
	}
	return false
}

func index_of_kind(list []events.Kind, kind events.Kind) int {
	for i, k := range list {
		if k == kind {
			return i
		}
	}
	return -1
}

// --- New_manager -----------------------------------------------------

func Test_new_manager_refuses_a_missing_dependency(t *testing.T) {
	env := new_test_env(t)
	deps := Dependencies{
		Control_plane: env.control_plane,
		Token:         env.token,
		Protocols:     env.protocols,
		Prober:        env.prober,
		Policy:        env.policy,
		Store:         env.store,
		Bus:           env.bus,
	}

	missing_bus := deps
	missing_bus.Bus = nil
	if _, err := New_manager(missing_bus); err == nil {
		t.Fatal("New_manager() error = nil, want a refusal for a missing Bus")
	}

	missing_policy := deps
	missing_policy.Policy = nil
	if _, err := New_manager(missing_policy); err == nil {
		t.Fatal("New_manager() error = nil, want a refusal for a missing Policy")
	}
}

func Test_new_manager_defaults_now_and_new_session_id(t *testing.T) {
	env := new_test_env(t)
	manager, err := New_manager(Dependencies{
		Control_plane: env.control_plane,
		Token:         env.token,
		Protocols:     env.protocols,
		Prober:        env.prober,
		Policy:        env.policy,
		Store:         env.store,
		Bus:           env.bus,
	})
	if err != nil {
		t.Fatalf("New_manager() error = %v, want Now and New_session_id to default", err)
	}
	if manager.State() != State_disconnected {
		t.Fatalf("State() = %v, want State_disconnected", manager.State())
	}
}

// --- provisioning: the tests that come first ---------------------------

func Test_first_connect_with_no_client_id_provisions_exactly_once(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()

	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	if env.control_plane.create_profile_calls != 1 {
		t.Fatalf("Create_profile calls = %d, want 1", env.control_plane.create_profile_calls)
	}
	if env.store.state.Client_id != "client-1" {
		t.Fatalf("stored Client_id = %q, want client-1", env.store.state.Client_id)
	}
}

func Test_second_connect_with_a_stored_client_id_never_creates_a_profile(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "existing-client"
	manager := env.build()

	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	if env.control_plane.create_profile_calls != 0 {
		t.Fatalf("Create_profile calls = %d, want 0", env.control_plane.create_profile_calls)
	}
	if env.control_plane.fetch_config_calls != 1 {
		t.Fatalf("Fetch_config calls = %d, want 1", env.control_plane.fetch_config_calls)
	}
}

func Test_client_not_found_from_fetch_config_provisions_again_exactly_once(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "stale-client"
	env.control_plane.fetch_config_sequence = []fetch_config_response{
		{err: &api.Error{Status: 404, Code: api.Code_client_not_found}},
	}
	env.control_plane.create_profile_result.Client_id = "fresh-client"
	manager := env.build()

	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	if env.control_plane.create_profile_calls != 1 {
		t.Fatalf("Create_profile calls = %d, want 1", env.control_plane.create_profile_calls)
	}
	if env.store.state.Client_id != "fresh-client" {
		t.Fatalf("stored Client_id = %q, want fresh-client", env.store.state.Client_id)
	}
}

func Test_a_connect_that_fails_after_provisioning_still_saves_the_new_client_id(t *testing.T) {
	env := new_test_env(t)
	env.protocol.connect_err = errors.New("adapter refused")
	env.config.Recovery.Max_switch_attempts = 1 // exhaust on the first failure
	manager := env.build()

	err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto})
	if err == nil {
		t.Fatal("Connect() error = nil, want a failure")
	}

	if env.store.state.Client_id != "client-1" {
		t.Fatalf("stored Client_id = %q, want client-1 even though the connect failed", env.store.state.Client_id)
	}
	if manager.State() != State_failed {
		t.Fatalf("State() = %v, want State_failed", manager.State())
	}
}

// --- the connect flow ----------------------------------------------------

func Test_the_states_go_disconnected_connecting_connected_in_order(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()

	if manager.State() != State_disconnected {
		t.Fatalf("initial State() = %v, want State_disconnected", manager.State())
	}

	ch, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if manager.State() != State_connected {
		t.Fatalf("final State() = %v, want State_connected", manager.State())
	}

	got := kinds_of(drain_events(ch))
	started := index_of_kind(got, events.Kind_connection_started)
	established := index_of_kind(got, events.Kind_connection_established)
	if started == -1 || established == -1 || started > established {
		t.Fatalf("events = %v, want Kind_connection_started before Kind_connection_established", got)
	}
}

func Test_verification_failure_leaves_the_manager_not_connected_and_the_protocol_disconnected(t *testing.T) {
	env := new_test_env(t)
	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: false}}})
	env.config.Recovery.Max_switch_attempts = 1
	manager := env.build()

	err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto})
	if err == nil {
		t.Fatal("Connect() error = nil, want a failure")
	}
	if manager.State() == State_connected {
		t.Fatalf("State() = %v, want anything but State_connected", manager.State())
	}
	if env.protocol.disconnect_calls == 0 {
		t.Fatal("protocol Disconnect() was never called after a failed verification")
	}
}

func Test_a_protocol_connect_failure_is_a_hard_failure_and_asks_the_policy_for_another_target(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.control_plane.recommend_result.Alternatives = []api.Alternative{{Node: "nl-02", Protocol: "wireguard"}}
	env.protocol.connect_errs = []error{errors.New("first attempt failed")}
	manager := env.build()

	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v, want the retry on nl-02 to succeed", err)
	}
	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected after the retry", manager.State())
	}
	if env.control_plane.recommend_calls != 1 {
		t.Fatalf("Recommend calls = %d, want 1: the retry reuses the alternatives already offered", env.control_plane.recommend_calls)
	}
	if env.control_plane.fetch_config_calls != 2 {
		t.Fatalf("Fetch_config calls = %d, want 2 (de-01 and the retried nl-02)", env.control_plane.fetch_config_calls)
	}
	session, ok := manager.Session()
	if !ok || session.Node_id != "nl-02" {
		t.Fatalf("Session() = %+v, %v, want node nl-02", session, ok)
	}
}

func Test_policy_budget_spent_lands_in_state_failed_with_kind_error(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.config.Recovery.Max_switch_attempts = 1
	env.protocol.connect_err = errors.New("adapter refused")
	manager := env.build()

	ch, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto})
	if err == nil {
		t.Fatal("Connect() error = nil, want Err_budget_spent to surface as a failure")
	}
	if manager.State() != State_failed {
		t.Fatalf("State() = %v, want State_failed", manager.State())
	}
	if !contains_kind(drain_events(ch), events.Kind_error) {
		t.Fatal("no Kind_error event was published")
	}
}

func Test_connect_while_connected_starts_no_second_tunnel(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	session_before, _ := manager.Session()

	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("second Connect() error = %v, want nil (a no-op)", err)
	}
	session_after, _ := manager.Session()
	if !reflect.DeepEqual(session_before, session_after) {
		t.Fatalf("Session() changed across a redundant Connect(): %+v -> %+v", session_before, session_after)
	}
	if env.control_plane.create_profile_calls != 1 {
		t.Fatalf("Create_profile calls = %d, want 1 (no second tunnel)", env.control_plane.create_profile_calls)
	}
}

func Test_connect_while_degraded_returns_err_busy(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second)) // a blip; the next bad sample confirms it
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	if manager.State() != State_degraded {
		t.Fatalf("State() = %v, want State_degraded", manager.State())
	}

	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); !errors.Is(err, Err_busy) {
		t.Fatalf("Connect() while degraded error = %v, want Err_busy", err)
	}
}

// --- disconnect ------------------------------------------------------

func Test_disconnect_when_disconnected_is_not_an_error(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()

	if err := manager.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect() error = %v, want nil", err)
	}
	if env.protocol.disconnect_calls != 0 {
		t.Fatalf("protocol Disconnect() was called %d times, want 0", env.protocol.disconnect_calls)
	}
}

// A control plane that does not answer holds a connect for its whole
// timeout. Disconnect -- and the "stay off" said with it -- must cut that
// short, not queue behind it.
func Test_disconnect_abandons_a_connect_waiting_on_the_control_plane(t *testing.T) {
	env := new_test_env(t)
	env.control_plane.recommend_hang = make(chan struct{})
	manager := env.build()

	connected := make(chan error, 1)
	go func() { connected <- manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}) }()
	<-env.control_plane.recommend_hang

	done := make(chan error, 2)
	go func() {
		done <- manager.Set_connect_on_start(false)
		done <- manager.Disconnect(context.Background())
	}()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("call %d error = %v, want nil", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("disconnect waited for the connect instead of abandoning it")
		}
	}
	if err := <-connected; err == nil {
		t.Fatal("the abandoned Connect() reported success")
	}
	if manager.State() != State_disconnected {
		t.Fatalf("State() = %v, want State_disconnected", manager.State())
	}
}

func Test_disconnect_after_a_failed_connect_is_not_refused(t *testing.T) {
	env := new_test_env(t)
	env.control_plane.recommend_err = errors.New("control plane down")
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err == nil {
		t.Fatal("Connect() error = nil, want the control plane's failure")
	}
	if manager.State() != State_failed {
		t.Fatalf("State() after the failed connect = %v, want State_failed", manager.State())
	}
	if err := manager.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect() error = %v, want nil", err)
	}
	if manager.State() != State_disconnected {
		t.Fatalf("State() = %v, want State_disconnected", manager.State())
	}
}

func Test_a_protocol_that_errors_on_disconnect_still_ends_disconnected(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	env.protocol.disconnect_err = errors.New("driver refused to let go")

	err := manager.Disconnect(context.Background())
	if err == nil {
		t.Fatal("Disconnect() error = nil, want the protocol's error surfaced as a Fault")
	}
	if manager.State() != State_disconnected {
		t.Fatalf("State() = %v, want State_disconnected even though the protocol errored", manager.State())
	}
}

// --- monitoring and recovery on Tick -------------------------------------

func Test_a_healthy_tick_emits_nothing(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	ch, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	manager.Tick(context.Background(), env.clock.advance(5*time.Second))

	if got := drain_events(ch); len(got) != 0 {
		t.Fatalf("healthy Tick() emitted %v, want nothing", kinds_of(got))
	}
	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected", manager.State())
	}
}

func Test_a_degraded_tick_enters_state_degraded_once_and_does_not_switch_before_the_window(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	degraded_sample := diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}
	env.prober.set([]prober_response{{sample: degraded_sample}})

	ch, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	manager.Tick(context.Background(), env.clock.advance(5*time.Second)) // a blip; the next bad sample confirms it
	manager.Tick(context.Background(), env.clock.advance(40*time.Second))
	if manager.State() != State_degraded {
		t.Fatalf("State() = %v, want State_degraded", manager.State())
	}
	first := kinds_of(drain_events(ch))
	if !kind_list_contains(first, events.Kind_connection_degraded) {
		t.Fatalf("events = %v, want Kind_connection_degraded on entry", first)
	}

	manager.Tick(context.Background(), env.clock.advance(7*time.Second)) // well within the 15s window
	if manager.State() != State_degraded {
		t.Fatalf("State() = %v, want State_degraded", manager.State())
	}
	second := drain_events(ch)
	if len(second) != 0 {
		t.Fatalf("Tick() before the window elapsed emitted %v, want nothing", kinds_of(second))
	}
	if env.control_plane.fetch_config_calls != 0 {
		t.Fatalf("Fetch_config calls = %d, want 0: no switch yet", env.control_plane.fetch_config_calls)
	}
}

func Test_still_degraded_after_the_window_switches_with_reason_degraded(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.control_plane.recommend_result.Alternatives = []api.Alternative{{Node: "nl-02", Protocol: "wireguard"}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	degraded_sample := diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}
	env.prober.set([]prober_response{{sample: degraded_sample}})

	manager.Tick(context.Background(), env.clock.advance(5*time.Second))  // a blip; the next bad sample confirms it
	manager.Tick(context.Background(), env.clock.advance(40*time.Second)) // enters degraded
	manager.Tick(context.Background(), env.clock.advance(16*time.Second)) // window elapsed, still stable long enough

	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected after the switch", manager.State())
	}
	session, ok := manager.Session()
	if !ok || session.Node_id != "nl-02" {
		t.Fatalf("Session() = %+v, %v, want the switch to have landed on nl-02", session, ok)
	}
	if env.protocol.disconnect_calls == 0 {
		t.Fatal("the old protocol was never stopped before switching")
	}
}

func Test_recovered_before_the_window_returns_to_connected_without_switching(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	session_before, _ := manager.Session()
	fetch_config_calls_before := env.control_plane.fetch_config_calls

	degraded_sample := diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}
	env.prober.set([]prober_response{{sample: degraded_sample}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second)) // a blip; the next bad sample confirms it
	manager.Tick(context.Background(), env.clock.advance(40*time.Second))
	if manager.State() != State_degraded {
		t.Fatalf("State() = %v, want State_degraded", manager.State())
	}

	env.prober.set([]prober_response{{sample: healthy_sample}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second)) // well within the 15s window

	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected after recovering", manager.State())
	}
	session_after, _ := manager.Session()
	if !reflect.DeepEqual(session_before, session_after) {
		t.Fatalf("Session() changed on a mere recovery: %+v -> %+v", session_before, session_after)
	}
	if env.control_plane.fetch_config_calls != fetch_config_calls_before {
		t.Fatalf("Fetch_config calls = %d, want %d: recovering must not switch", env.control_plane.fetch_config_calls, fetch_config_calls_before)
	}
}

func Test_a_lost_tick_retries_the_current_target_once_before_switching(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	session_before, _ := manager.Session()

	lost_sample := diagnostics.Sample{Tunnel_up: false}
	env.prober.set([]prober_response{{sample: lost_sample}, {sample: healthy_sample}})

	ch, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	manager.Tick(context.Background(), env.clock.advance(5*time.Second))

	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected: the retry should have carried on", manager.State())
	}
	session_after, _ := manager.Session()
	if !reflect.DeepEqual(session_before, session_after) {
		t.Fatalf("Session() changed on a successful retry: %+v -> %+v", session_before, session_after)
	}
	if env.protocol.connect_calls != 2 {
		t.Fatalf("protocol Connect calls = %d, want 2 (the original connect and the one retry)", env.protocol.connect_calls)
	}
	if !kind_list_contains(kinds_of(drain_events(ch)), events.Kind_connection_lost) {
		t.Fatal("Kind_connection_lost was never published")
	}
}

func Test_the_retry_on_lost_path_leaves_the_attempt_counter_standing(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.policy.Attempts() != 0 {
		t.Fatalf("Attempts() after Connect = %d, want 0", env.policy.Attempts())
	}

	lost_sample := diagnostics.Sample{Tunnel_up: false}
	env.prober.set([]prober_response{{sample: lost_sample}, {sample: lost_sample}})
	// index 0 is the monitoring probe that finds it lost; index 1 is the
	// retry's own Verify probe, which finds it still down. No alternatives
	// are configured, so the ensuing switch has nowhere to go.

	manager.Tick(context.Background(), env.clock.advance(5*time.Second))

	if manager.State() != State_failed {
		t.Fatalf("State() = %v, want State_failed", manager.State())
	}
	if env.policy.Attempts() != 1 {
		t.Fatalf("Attempts() = %d, want 1: nothing on this path may call Reset()", env.policy.Attempts())
	}
}

func Test_a_single_probe_error_changes_nothing_the_second_is_treated_as_lost(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	var log_output bytes.Buffer
	manager.logger = slog.New(slog.NewTextHandler(&log_output, nil))
	probe_err := errors.New("probe: temporarily unreachable")
	env.prober.set([]prober_response{{err: probe_err}})

	ch, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected: one probe error is not a reason to move a connection", manager.State())
	}
	if got := drain_events(ch); len(got) != 0 {
		t.Fatalf("a single probe error emitted %v, want nothing", kinds_of(got))
	}

	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	if !kind_list_contains(kinds_of(drain_events(ch)), events.Kind_connection_lost) {
		t.Fatal("a second consecutive probe error was not treated as lost")
	}
	for _, count := range []string{"consecutive_failures=1", "consecutive_failures=2", "recovery_threshold=2"} {
		if !strings.Contains(log_output.String(), count) {
			t.Fatalf("missing counter %s: %s", count, &log_output)
		}
	}
}

func Test_a_better_recommendation_during_a_healthy_connection_emits_better_option_and_does_not_switch(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.config.Reevaluation_interval = time.Minute
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	session_before, _ := manager.Session()
	fetch_config_calls_before := env.control_plane.fetch_config_calls

	env.control_plane.recommend_result = api.Recommendation{Node_id: "nl-02", Protocol: "wireguard"}

	ch, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	manager.Tick(context.Background(), env.clock.advance(90*time.Second)) // past the 1m reevaluation interval

	got := drain_events(ch)
	if !contains_kind(got, events.Kind_better_option) {
		t.Fatalf("events = %v, want Kind_better_option", kinds_of(got))
	}
	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected: an offer must not switch anything", manager.State())
	}
	session_after, _ := manager.Session()
	if !reflect.DeepEqual(session_before, session_after) {
		t.Fatalf("Session() changed on a mere offer: %+v -> %+v", session_before, session_after)
	}
	if env.control_plane.fetch_config_calls != fetch_config_calls_before {
		t.Fatalf("Fetch_config calls = %d, want %d: offering is not switching", env.control_plane.fetch_config_calls, fetch_config_calls_before)
	}
}

func Test_manual_mode_does_not_call_recommend_and_does_not_switch_when_degraded(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()

	request := Connect_request{Mode: Mode_manual, Node_id: "de-01", Protocol: "wireguard"}
	if err := manager.Connect(context.Background(), request); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.control_plane.recommend_calls != 0 {
		t.Fatalf("Recommend calls = %d, want 0 in manual mode", env.control_plane.recommend_calls)
	}
	fetch_config_calls_before := env.control_plane.fetch_config_calls

	degraded_sample := diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}
	env.prober.set([]prober_response{{sample: degraded_sample}})

	manager.Tick(context.Background(), env.clock.advance(40*time.Second))
	manager.Tick(context.Background(), env.clock.advance(20*time.Second)) // past the window

	if manager.State() != State_degraded {
		t.Fatalf("State() = %v, want State_degraded: manual mode reports but does not move the person off their chosen node", manager.State())
	}
	if env.control_plane.recommend_calls != 0 {
		t.Fatalf("Recommend calls = %d, want 0: manual mode never asks for a recommendation", env.control_plane.recommend_calls)
	}
	if env.control_plane.fetch_config_calls != fetch_config_calls_before {
		t.Fatalf("Fetch_config calls = %d, want %d: manual mode must not switch on degradation", env.control_plane.fetch_config_calls, fetch_config_calls_before)
	}
}

func Test_last_node_id_and_last_protocol_are_saved_on_success(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()

	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	if env.store.state.Last_node_id != "de-01" || env.store.state.Last_protocol != "wireguard" {
		t.Fatalf("stored state = %+v, want Last_node_id de-01 and Last_protocol wireguard", env.store.state)
	}
}

// A connection that degrades soon after coming up must not be declared
// failed. The policy's "not yet" is a wait, not a verdict: the stability
// window is there to stop flapping, and turning it into a permanent failure
// state would use an anti-flapping guard to break the very connection it was
// protecting -- while traffic is still flowing through it.
func Test_a_degraded_connection_inside_the_stability_window_waits_instead_of_failing(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.control_plane.recommend_result.Alternatives = []api.Alternative{{Node: "nl-02", Protocol: "wireguard"}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	subscription, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	degraded_sample := diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}
	env.prober.set([]prober_response{{sample: degraded_sample}})

	manager.Tick(context.Background(), env.clock.advance(5*time.Second))  // enters degraded
	manager.Tick(context.Background(), env.clock.advance(16*time.Second)) // window elapsed, but only 21s stable

	if manager.State() != State_degraded {
		t.Fatalf("State() = %v, want State_degraded: the stability window says wait, not fail", manager.State())
	}
	if contains_kind(drain_events(subscription), events.Kind_error) {
		t.Fatal("waiting for the stability window was reported as an error")
	}

	// And the wait ends rather than jams: past Min_stable_time the switch it
	// was holding back happens.
	manager.Tick(context.Background(), env.clock.advance(15*time.Second))
	if manager.State() != State_connected {
		t.Fatalf("State() = %v, want State_connected once the window has passed", manager.State())
	}
	session, ok := manager.Session()
	if !ok || session.Node_id != "nl-02" {
		t.Fatalf("Session() = %+v, %v, want the delayed switch to have landed on nl-02", session, ok)
	}
}

// Recovery finding nowhere to go is not the same as the connection being
// gone. A degraded tunnel still carries a person's traffic, and reporting
// "failed" while it does would be a lie they would act on -- by reconnecting
// something that is already working, badly.
func Test_a_degraded_connection_with_nowhere_to_go_stays_degraded(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.control_plane.recommend_result.Alternatives = nil
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	session_before, _ := manager.Session()
	subscription, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	degraded_sample := diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}
	env.prober.set([]prober_response{{sample: degraded_sample}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second)) // a blip; the next bad sample confirms it
	manager.Tick(context.Background(), env.clock.advance(40*time.Second))
	manager.Tick(context.Background(), env.clock.advance(16*time.Second))

	if manager.State() != State_degraded {
		t.Fatalf("State() = %v, want State_degraded: a degraded tunnel is still a tunnel", manager.State())
	}
	session_after, ok := manager.Session()
	if !ok || !reflect.DeepEqual(session_before, session_after) {
		t.Fatalf("Session() = %+v, %v: the session was dropped although nothing replaced it", session_after, ok)
	}
	if !contains_kind(drain_events(subscription), events.Kind_error) {
		t.Fatal("recovery ran out of targets and said nothing about it")
	}
}

// Run builds a ticker straight from Monitor_interval, and a non-positive
// interval panics. That panic would happen inside a service that owns a
// person's network route, so a Config nobody filled in must not be able to
// reach it.
func Test_a_manager_built_without_a_config_gets_usable_intervals(t *testing.T) {
	env := new_test_env(t)
	env.config = Config{}
	manager := env.build()

	if manager.config.Monitor_interval <= 0 {
		t.Fatalf("Monitor_interval = %v, which panics time.NewTicker", manager.config.Monitor_interval)
	}
	if manager.config.Profile_duration_days <= 0 {
		t.Fatalf("Profile_duration_days = %d: a profile is provisioned for that many days", manager.config.Profile_duration_days)
	}
	if manager.config.Recovery.Max_switch_attempts <= 0 {
		t.Fatalf("Max_switch_attempts = %d: recovery would give up before trying", manager.config.Recovery.Max_switch_attempts)
	}
}

func Test_every_state_change_is_counted_and_a_no_op_is_not(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()

	if got := manager.Transitions(); got != 0 {
		t.Fatalf("Transitions() on a new manager = %d, want 0", got)
	}
	if err := manager.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	if got := manager.Transitions(); got != 0 {
		t.Fatalf("Transitions() after a disconnect that changed nothing = %d, want 0", got)
	}
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if got := manager.Transitions(); got != 2 { // disconnected -> connecting -> connected
		t.Fatalf("Transitions() after a connect = %d, want 2", got)
	}
}

// --- Network_changed -----------------------------------------------------

func Test_network_changed_on_a_connected_repinner_repins_then_ticks(t *testing.T) {
	env := new_test_env(t)
	repinner := &repinning_protocol{fake_protocol: env.protocol}
	env.protocols.wrap = func(*fake_protocol) protocols.Protocol { return repinner }
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	probes := env.prober.calls

	manager.Network_changed(context.Background(), env.clock.advance(5*time.Second))

	if repinner.repin_calls != 1 {
		t.Fatalf("Repin called %d times, want 1", repinner.repin_calls)
	}
	if env.prober.calls != probes+1 {
		t.Fatalf("probes = %d, want one monitoring pass (%d)", env.prober.calls, probes+1)
	}
}

func Test_network_changed_survives_a_failed_repin_and_still_ticks(t *testing.T) {
	env := new_test_env(t)
	repinner := &repinning_protocol{fake_protocol: env.protocol, repin_err: errors.New("no route")}
	env.protocols.wrap = func(*fake_protocol) protocols.Protocol { return repinner }
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	probes := env.prober.calls

	manager.Network_changed(context.Background(), env.clock.advance(5*time.Second))

	if env.prober.calls != probes+1 {
		t.Fatalf("probes = %d, want one monitoring pass (%d)", env.prober.calls, probes+1)
	}
}

func Test_network_changed_without_a_repinner_only_ticks(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	probes := env.prober.calls

	manager.Network_changed(context.Background(), env.clock.advance(5*time.Second))

	if env.prober.calls != probes+1 {
		t.Fatalf("probes = %d, want one monitoring pass (%d)", env.prober.calls, probes+1)
	}
}

func Test_network_changed_while_disconnected_does_nothing(t *testing.T) {
	env := new_test_env(t)
	repinner := &repinning_protocol{fake_protocol: env.protocol}
	env.protocols.wrap = func(*fake_protocol) protocols.Protocol { return repinner }
	manager := env.build()

	manager.Network_changed(context.Background(), env.clock.advance(5*time.Second))

	if repinner.repin_calls != 0 || env.prober.calls != 0 {
		t.Fatalf("repin = %d, probes = %d; want neither", repinner.repin_calls, env.prober.calls)
	}
}

// A match holds a worse but working connection where it is: leaving the
// node would end the match. When the match ends the switch goes ahead.
func Test_a_match_holds_a_degraded_connection_on_its_node(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.control_plane.recommend_result.Alternatives = []api.Alternative{{Node: "nl-02", Protocol: "wireguard"}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	before, _ := manager.Session()
	manager.Set_in_match(true)

	degraded_sample := diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Packet_loss: 0.5}
	env.prober.set([]prober_response{{sample: degraded_sample}})
	manager.Tick(context.Background(), env.clock.advance(40*time.Second))
	manager.Tick(context.Background(), env.clock.advance(16*time.Second))
	manager.Tick(context.Background(), env.clock.advance(16*time.Second))

	if manager.State() != State_degraded {
		t.Fatalf("State() = %v, want State_degraded: held on the node", manager.State())
	}
	if after, _ := manager.Session(); after.Node_id != before.Node_id {
		t.Fatalf("switched to %q during a match", after.Node_id)
	}

	manager.Set_in_match(false)
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	if after, _ := manager.Session(); after.Node_id != "nl-02" {
		t.Fatalf("Session() node = %q after the match, want the switch to nl-02", after.Node_id)
	}
}

// A better node is not offered during a match.
func Test_no_better_option_is_offered_during_a_match(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	env.config.Reevaluation_interval = time.Minute
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	env.control_plane.recommend_result = api.Recommendation{Node_id: "nl-02", Protocol: "wireguard"}
	manager.Set_in_match(true)

	ch, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()
	manager.Tick(context.Background(), env.clock.advance(90*time.Second))
	if got := drain_events(ch); contains_kind(got, events.Kind_better_option) {
		t.Fatalf("events = %v, want no offer during a match", kinds_of(got))
	}

	manager.Set_in_match(false)
	manager.Tick(context.Background(), env.clock.advance(time.Second))
	if got := drain_events(ch); !contains_kind(got, events.Kind_better_option) {
		t.Fatalf("events = %v, want the offer once the match ended", kinds_of(got))
	}
}

func Test_a_rebuild_shows_as_switching_and_a_lost_manual_tunnel_is_not_connected(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_manual, Node_id: "de-01", Protocol: "wireguard"}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	ch, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()

	// Lost, and the retry on the chosen server does not come back.
	env.protocol.connect_err = errors.New("no handshake")
	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: false}}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))

	if state := manager.State(); state == State_connected || state == State_degraded || state == State_switching {
		t.Fatalf("State() = %v after the chosen server stopped answering, want it no longer connected", state)
	}
	var states []State
	for _, e := range drain_events(ch) {
		if change, ok := e.Data.(State_change); ok && e.Kind == events.Kind_state_changed {
			states = append(states, change.To)
		}
	}
	saw_switching := false
	for _, state := range states {
		saw_switching = saw_switching || state == State_switching
	}
	if !saw_switching {
		t.Fatalf("states published = %v, want switching while the tunnel was rebuilt", states)
	}
}

func Test_a_target_lost_again_soon_after_a_retry_is_not_retried_again(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_manual, Node_id: "de-01", Protocol: "wireguard"}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	lost := prober_response{sample: diagnostics.Sample{Tunnel_up: false}}
	env.prober.set([]prober_response{lost, {sample: healthy_sample}, lost})

	manager.Tick(context.Background(), env.clock.advance(5*time.Second)) // lost, retried, verified
	if manager.State() != State_connected || env.protocol.connect_calls != 2 {
		t.Fatalf("after the first loss: state %v, connects %d; want connected after one retry", manager.State(), env.protocol.connect_calls)
	}
	manager.Tick(context.Background(), env.clock.advance(8*time.Second)) // lost again at once
	if env.protocol.connect_calls != 2 {
		t.Fatalf("connects = %d, want no second retry of a target that just dropped again", env.protocol.connect_calls)
	}
	if manager.State() == State_connected {
		t.Fatal("still connected after the chosen server dropped twice")
	}
}

func Test_amneziawg_orders_before_plain_wireguard(t *testing.T) {
	offered := []recovery.Target{{Node_id: "n", Protocol: "wireguard"}, {Node_id: "n", Protocol: "amneziawg"}}
	slices.SortStableFunc(offered, func(a, b recovery.Target) int { return obfuscated_first(a.Protocol) - obfuscated_first(b.Protocol) })
	if offered[0].Protocol != "amneziawg" {
		t.Fatalf("first = %s, want amneziawg", offered[0].Protocol)
	}
}
