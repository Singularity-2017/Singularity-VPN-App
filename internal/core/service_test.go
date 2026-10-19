package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/security"
)

const test_password = "correct-horse-battery-staple"

func sign_in_params() ipc.Sign_in_params {
	return ipc.Sign_in_params{Username: "alice", Password: test_password}
}

func connect_params_auto() ipc.Connect_params {
	return ipc.Connect_params{Mode: ipc.Mode_auto}
}

// --- status --------------------------------------------------------------

func Test_a_status_before_sign_in_says_so(t *testing.T) {
	f := new_fixture(t)

	got, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
	if got.Signed_in {
		t.Fatalf("Signed_in = true before any sign-in, want false")
	}
	if got.State != string(connection.State_disconnected) {
		t.Fatalf("State = %q, want %q", got.State, connection.State_disconnected)
	}
	if got.Since != nil {
		t.Fatalf("Since = %v with no connection, want it absent", got.Since)
	}
}

func Test_a_status_after_sign_in_says_signed_in(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)

	got, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
	if !got.Signed_in {
		t.Fatalf("Signed_in = false after a successful sign-in, want true")
	}
}

// Test_a_status_reports_the_session_it_has covers the other half of the
// Since decision: once there is a connection there is a real moment to
// report, and it must actually arrive.
func Test_a_status_reports_the_session_it_has(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)

	got, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
	if got.State != string(connection.State_connected) {
		t.Fatalf("State = %q, want %q", got.State, connection.State_connected)
	}
	if got.Node_id != "node-1" || got.Protocol != "wireguard" {
		t.Fatalf("Status() = %+v, want the node and protocol of the session", got)
	}
	if got.Session_id == "" {
		t.Fatalf("Session_id is empty while connected, want the session's own id")
	}
	if got.Since == nil {
		t.Fatalf("Since is absent while connected, want the moment the session started")
	}
}

// reconnect_through_recommend disconnects and connects again: the second
// connect has a client_id, so it asks Recommend instead of Create_profile.
func reconnect_through_recommend(t *testing.T, f *fixture, recommendation api.Recommendation) {
	t.Helper()
	f.control_plane.recommendation = recommendation
	f.control_plane.config = api.Vpn_config{Config: "[Interface]\n", Protocol: recommendation.Protocol, Expires_at: time.Now().Add(24 * time.Hour)}
	if err := f.app.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if err := f.app.Connect(context.Background(), connect_params_auto()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

func Test_a_status_names_the_place_the_control_plane_named(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)
	reconnect_through_recommend(t, f, api.Recommendation{
		Node_id: "node-1", Node_name: "de-fra-2", Country: "Germany", City: "Frankfurt", Protocol: "wireguard",
	})

	got, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Node_name != "de-fra-2" || got.Country != "Germany" || got.City != "Frankfurt" {
		t.Fatalf("Status() place = %q/%q/%q, want the recommendation's description of the node", got.Node_name, got.Country, got.City)
	}
}

// Test_a_node_the_recommendation_did_not_name_has_no_place: the manager
// remembers the last recommendation, and a session on any other node --
// a manual choice here, an alternative after a switch -- must not borrow
// its description, or a person would see a place they are not connected
// to.
func Test_a_node_the_recommendation_did_not_name_has_no_place(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)
	reconnect_through_recommend(t, f, api.Recommendation{Node_id: "node-9", Country: "Netherlands", City: "Amsterdam", Protocol: "wireguard"})
	if err := f.app.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	manual := ipc.Connect_params{Mode: ipc.Mode_manual, Node_id: "node-1", Protocol: "wireguard"}
	if err := f.app.Connect(context.Background(), manual); err != nil {
		t.Fatalf("Connect manual: %v", err)
	}

	got, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Node_name != "" || got.Country != "" || got.City != "" {
		t.Fatalf("Status() place = %q/%q/%q, want none for a node no recommendation described", got.Node_name, got.Country, got.City)
	}
}

func Test_a_status_carries_no_numbers_until_the_session_is_measured(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)

	got, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Rtt_ms != nil || got.Loss_percent != nil || got.Measured_at != nil {
		t.Fatalf("Status() before any probe = rtt %v loss %v at %v, want all absent", got.Rtt_ms, got.Loss_percent, got.Measured_at)
	}
}

func Test_a_status_reports_the_last_measurement(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)
	taken := time.Now()
	f.prober.sample = diagnostics.Sample{Rtt: 44 * time.Millisecond, Packet_loss: 0.25, Internet_ok: true, Tunnel_up: true, Dns_ok: true, Taken_at: taken}
	f.app.Recheck(context.Background())

	got, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Rtt_ms == nil || *got.Rtt_ms != 44 {
		t.Fatalf("Rtt_ms = %v, want 44", got.Rtt_ms)
	}
	if got.Loss_percent == nil || *got.Loss_percent != 25 {
		t.Fatalf("Loss_percent = %v, want 25", got.Loss_percent)
	}
	if got.Measured_at == nil || !got.Measured_at.Equal(taken) {
		t.Fatalf("Measured_at = %v, want %v", got.Measured_at, taken)
	}

	// A new session starts unmeasured: the old node's numbers are not this
	// node's.
	reconnect_through_recommend(t, f, api.Recommendation{Node_id: "node-2", Protocol: "wireguard"})
	got, err = f.app.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Rtt_ms != nil {
		t.Fatalf("Rtt_ms after a new session = %v, want absent until it is measured", *got.Rtt_ms)
	}
}

// Test_a_store_that_cannot_be_read_is_not_a_person_who_is_signed_out: a
// locked or corrupted secret store is a fault, and reporting it as "signed
// out" would send somebody to type their password again to fix a file.
func Test_a_store_that_cannot_be_read_is_not_a_person_who_is_signed_out(t *testing.T) {
	f := new_fixture(t)
	broken := errors.New("the store is locked")
	f.app.secrets = &failing_store{err: broken}

	_, err := f.app.Status(context.Background())
	if err == nil {
		t.Fatal("Status() with an unreadable store = nil error, want the failure reported")
	}
	if errors.Is(err, ipc.Err_not_signed_in) {
		t.Fatalf("Status() with an unreadable store = %v, want a fault rather than not_signed_in", err)
	}
}

// failing_store answers every call with one error that is not
// security.Err_not_found.
type failing_store struct{ err error }

func (s *failing_store) Get(key string) ([]byte, error) { return nil, s.err }
func (s *failing_store) Set(key string, v []byte) error { return s.err }
func (s *failing_store) Delete(key string) error        { return s.err }

// --- sign in -------------------------------------------------------------

func Test_sign_in_stores_the_device_token_and_not_the_password(t *testing.T) {
	f := new_fixture(t)

	result, err := f.app.Sign_in(context.Background(), sign_in_params())
	if err != nil {
		t.Fatalf("Sign_in() error = %v, want nil", err)
	}
	if result.Device_id != "device-1" {
		t.Fatalf("Device_id = %q, want %q", result.Device_id, "device-1")
	}

	token, err := f.secrets.Get(security.Key_device_token)
	if err != nil {
		t.Fatalf("the device token was not stored: %v", err)
	}
	if string(token) != "device-token" {
		t.Fatalf("stored device token = %q, want %q", token, "device-token")
	}
	if _, err := f.secrets.Get(security.Key_device_id); err != nil {
		t.Fatalf("the device id was not stored: %v", err)
	}

	// Nothing anywhere in the store may be, or contain, the password.
	for _, key := range []string{security.Key_device_token, security.Key_device_id} {
		value, err := f.secrets.Get(key)
		if err != nil {
			continue
		}
		if bytes.Contains(value, []byte(test_password)) {
			t.Fatalf("the store holds the password under %q, want it stored nowhere at all", key)
		}
	}
	if strings.Contains(f.store.get().Client_id+f.store.get().Device_id, test_password) {
		t.Fatal("the local state holds the password, want it stored nowhere at all")
	}
}

func Test_sign_in_records_the_device_id_in_the_local_state(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)

	if got := f.store.get().Device_id; got != "device-1" {
		t.Fatalf("state.Device_id = %q after sign-in, want %q", got, "device-1")
	}
}

func Test_sign_in_mints_the_token_under_the_configured_device_name(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)

	if len(f.control_plane.device_for) != 1 || f.control_plane.device_for[0] != "test machine" {
		t.Fatalf("device tokens minted under %v, want exactly one under %q -- a token nobody can identify is one nobody can revoke",
			f.control_plane.device_for, "test machine")
	}
}

func Test_sign_in_rejects_bad_credentials_as_unauthorized(t *testing.T) {
	f := new_fixture(t)
	f.control_plane.login_err = &api.Error{Status: 401, Code: api.Code_unauthorized, Message: "bad credentials"}

	_, err := f.app.Sign_in(context.Background(), sign_in_params())
	if !errors.Is(err, ipc.Err_unauthorized) {
		t.Fatalf("Sign_in() with bad credentials = %v, want ipc.Err_unauthorized", err)
	}
	if _, err := f.secrets.Get(security.Key_device_token); !errors.Is(err, security.Err_not_found) {
		t.Fatal("a failed sign-in left a device token behind, want nothing stored")
	}
}

// Test_a_device_token_that_cannot_be_minted_keeps_its_own_message: the
// control plane refusing to mint is a real answer about the account, not an
// internal failure, and flattening it would leave the person with nothing
// to act on.
func Test_a_device_token_that_cannot_be_minted_keeps_its_own_message(t *testing.T) {
	f := new_fixture(t)
	f.control_plane.device_err = &api.Error{
		Status:  409,
		Code:    api.Code_device_token_cannot_mint,
		Message: "this account already has the maximum number of devices",
	}

	_, err := f.app.Sign_in(context.Background(), sign_in_params())
	if err == nil {
		t.Fatal("Sign_in() = nil error, want the refusal")
	}
	if !strings.Contains(err.Error(), "maximum number of devices") {
		t.Fatalf("Sign_in() error = %q, want it to carry the control plane's own message", err)
	}
	if errors.Is(err, ipc.Err_unauthorized) {
		t.Fatalf("Sign_in() error = %v, want it not to read as a wrong password", err)
	}
}

func Test_a_sign_in_result_carries_no_token(t *testing.T) {
	f := new_fixture(t)
	result, err := f.app.Sign_in(context.Background(), sign_in_params())
	if err != nil {
		t.Fatalf("Sign_in() error = %v", err)
	}

	value := reflect.ValueOf(result)
	for i := 0; i < value.NumField(); i++ {
		name := value.Type().Field(i).Name
		if strings.Contains(strings.ToLower(name), "token") {
			t.Fatalf("Sign_in_result has field %q, want the token never to leave the service", name)
		}
		if field := value.Field(i); field.Kind() == reflect.String && field.String() == "device-token" {
			t.Fatalf("Sign_in_result.%s carries the device token, want the token never to leave the service", name)
		}
	}
}

// --- sign out ------------------------------------------------------------

func Test_sign_out_removes_both_keys(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)

	if err := f.app.Sign_out(context.Background()); err != nil {
		t.Fatalf("Sign_out() = %v, want nil", err)
	}
	for _, key := range []string{security.Key_device_token, security.Key_device_id} {
		if _, err := f.secrets.Get(key); !errors.Is(err, security.Err_not_found) {
			t.Fatalf("%s survived Sign_out (err = %v), want it removed", key, err)
		}
	}
	if got := f.store.get().Device_id; got != "" {
		t.Fatalf("state.Device_id = %q after Sign_out, want it cleared", got)
	}
}

// Test_sign_out_leaves_the_client_id_alone guards a decision that looks
// like an oversight until it is followed through. The client_id names a
// profile the account has already paid a client_limit slot for; clearing it
// orphans that slot and the person cannot get it back. Signing in as a
// different account is handled a level down -- the control plane answers
// CLIENT_NOT_FOUND for somebody else's client_id, and
// connection.Manager.provision re-provisions exactly once when it sees that.
func Test_sign_out_leaves_the_client_id_alone(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)

	if got := f.store.get().Client_id; got == "" {
		t.Fatal("the fixture did not provision a client_id, so this test would pass for the wrong reason")
	}

	if err := f.app.Sign_out(context.Background()); err != nil {
		t.Fatalf("Sign_out() = %v, want nil", err)
	}
	if got := f.store.get().Client_id; got != "client-1" {
		t.Fatalf("Client_id = %q after Sign_out, want %q kept -- clearing it orphans a profile slot the account has already spent and cannot reclaim",
			got, "client-1")
	}
}

func Test_sign_out_takes_the_tunnel_down_first(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)

	if err := f.app.Sign_out(context.Background()); err != nil {
		t.Fatalf("Sign_out() = %v, want nil", err)
	}
	if got := f.protocol.disconnects(); got != 1 {
		t.Fatalf("the protocol was disconnected %d times during Sign_out, want 1 -- credentials must not be removed from under a live tunnel", got)
	}
}

// --- connect -------------------------------------------------------------

func Test_connect_refuses_a_mode_it_does_not_know(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)

	err := f.app.Connect(context.Background(), ipc.Connect_params{Mode: "sideways"})
	if err == nil {
		t.Fatal("Connect() with an unknown mode = nil error, want a refusal")
	}
	if f.protocol.connect_calls != 0 {
		t.Fatalf("the protocol was asked to connect %d times for an unknown mode, want 0", f.protocol.connect_calls)
	}
}

// Test_an_unknown_mode_does_not_become_auto tests connection_mode directly.
// It cannot be reached through Connect -- ipc.Connect_params.Validate
// refuses an unknown mode before the mapping is consulted -- and that is
// precisely why it needs its own test: connection.Mode's zero value is
// Mode_auto, so a mapping that stopped refusing would turn a request nobody
// can make today into a connection somewhere nobody asked for, silently,
// the first time a new mode is added to the protocol.
func Test_an_unknown_mode_does_not_become_auto(t *testing.T) {
	if _, err := connection_mode("sideways"); err == nil {
		t.Fatal(`connection_mode("sideways") = nil error, want a refusal rather than the zero value, which is auto`)
	}

	mode, err := connection_mode(ipc.Mode_auto)
	if err != nil || mode != connection.Mode_auto {
		t.Fatalf("connection_mode(auto) = %v, %v, want connection.Mode_auto", mode, err)
	}
	mode, err = connection_mode(ipc.Mode_manual)
	if err != nil || mode != connection.Mode_manual {
		t.Fatalf("connection_mode(manual) = %v, %v, want connection.Mode_manual", mode, err)
	}
}

// --- version -------------------------------------------------------------

func Test_version_omits_what_the_build_did_not_supply(t *testing.T) {
	f := new_fixture(t)

	got, err := f.app.Version(context.Background())
	if err != nil {
		t.Fatalf("Version() error = %v, want nil", err)
	}
	// A plain `go build` supplies none of these, and none of them may be
	// filled in with a placeholder: a version the build did not supply is
	// not printed.
	for name, value := range map[string]string{"Version": got.Version, "Commit": got.Commit, "Built": got.Built} {
		if value == "unknown" || value == "dev" || value == "-" {
			t.Fatalf("Version().%s = %q, want an empty string rather than a placeholder", name, value)
		}
	}
}

// --- error mapping -------------------------------------------------------

func Test_a_service_error_becomes_its_sentinel(t *testing.T) {
	not_found := fmt_errorf_wrapping(security.Err_not_found)

	cases := []struct {
		name string
		in   error
		want error
	}{
		{"nil stays nil", nil, nil},
		{"a missing secret is not signed in", not_found, ipc.Err_not_signed_in},
		{"a busy manager is busy", connection.Err_busy, ipc.Err_busy},
		{"an unauthorized control plane is unauthorized",
			&api.Error{Status: 401, Code: api.Code_unauthorized}, ipc.Err_unauthorized},
		{"an authentication fault is unauthorized",
			&connection.Fault{Kind: connection.Fault_authentication, Op: "connect", Err: errors.New("no")},
			ipc.Err_unauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := map_error(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("map_error(%v) = %v, want nil", tc.in, got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("map_error(%v) = %v, want it to match %v", tc.in, got, tc.want)
			}
		})
	}
}

// Test_a_missing_token_is_not_reported_as_a_wrong_password is the ordering
// guard in map_error. connection.Manager.Connect classifies anything its
// Token_source refuses as Fault_authentication, so a machine that has never
// been signed in arrives here wearing the same kind as a rejected password.
// The interface acts on the difference: one shows a sign-in form, the other
// says the password is wrong.
func Test_a_missing_token_is_not_reported_as_a_wrong_password(t *testing.T) {
	fault := &connection.Fault{
		Kind: connection.Fault_authentication,
		Op:   "connect",
		Err:  fmt_errorf_wrapping(security.Err_not_found),
	}

	got := map_error(fault)
	if !errors.Is(got, ipc.Err_not_signed_in) {
		t.Fatalf("map_error(authentication fault over a missing token) = %v, want ipc.Err_not_signed_in", got)
	}
	if errors.Is(got, ipc.Err_unauthorized) {
		t.Fatalf("map_error(authentication fault over a missing token) = %v, want it not to read as a rejected password", got)
	}
}

// Test_an_unrecognised_error_survives_unchanged: the pipe renders it as
// Error_internal carrying its own text, which is the right outcome on a
// boundary between two processes on the same machine.
func Test_an_unrecognised_error_survives_unchanged(t *testing.T) {
	boom := errors.New("the disk caught fire")
	if got := map_error(boom); !errors.Is(got, boom) {
		t.Fatalf("map_error(%v) = %v, want the error passed through", boom, got)
	}
}

// fmt_errorf_wrapping builds an error that wraps target with context, the
// way every layer in this application does, so the tests exercise
// errors.Is through a chain rather than against a bare sentinel.
func fmt_errorf_wrapping(target error) error {
	return fmt.Errorf("core: reading the device token: %w", target)
}

// --- resuming at start ---------------------------------------------------

// restart builds a fresh Application over the same store, secrets, control
// plane and tunnel, the way a reboot brings the service back over the same
// files.
func restart(t *testing.T, f *fixture) *fixture {
	t.Helper()
	registry, err := protocols.New_registry(f.protocol)
	if err != nil {
		t.Fatalf("building the protocol registry: %v", err)
	}
	bus := events.New_bus(16)
	app, err := New_application(Dependencies{
		Control_plane: f.control_plane,
		Secrets:       f.secrets,
		Store:         f.store,
		Protocols:     registry,
		Prober:        f.prober,
		Reach_prober:  f.reach_prober,
		Bus:           bus,
		Device_name:   "test machine",
	})
	if err != nil {
		t.Fatalf("New_application: %v", err)
	}
	t.Cleanup(func() { bus.Close() })
	next := *f
	next.app = app
	next.bus = bus
	return &next
}

func resume_at_once(t *testing.T) {
	t.Helper()
	saved := resume_delays
	resume_delays = []time.Duration{0}
	t.Cleanup(func() { resume_delays = saved })
}

func prepare_reconnect(f *fixture) {
	f.control_plane.recommendation = api.Recommendation{Node_id: "node-1", Protocol: "wireguard"}
	f.control_plane.config = api.Vpn_config{Config: "[Interface]\n", Protocol: "wireguard", Expires_at: time.Now().Add(24 * time.Hour)}
}

func Test_a_machine_left_connected_reconnects_at_start(t *testing.T) {
	resume_at_once(t)
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)
	if err := f.app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	prepare_reconnect(f)
	g := restart(t, f)
	g.app.resume(context.Background())

	if got := g.app.manager.State(); got != connection.State_connected {
		t.Fatalf("state after a restart of a machine left connected = %q, want connected", got)
	}
}

func Test_a_person_who_disconnected_stays_disconnected_at_start(t *testing.T) {
	resume_at_once(t)
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)
	if err := f.app.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if err := f.app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	prepare_reconnect(f)
	g := restart(t, f)
	g.app.resume(context.Background())

	if got := g.app.manager.State(); got != connection.State_disconnected {
		t.Fatalf("state after a restart following a disconnect = %q, want disconnected", got)
	}
}

func Test_signing_out_withdraws_the_wish_to_be_connected(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)
	if err := f.app.Sign_out(context.Background()); err != nil {
		t.Fatalf("Sign_out: %v", err)
	}
	want, err := f.app.manager.Connect_on_start()
	if err != nil {
		t.Fatalf("Connect_on_start: %v", err)
	}
	if want {
		t.Fatal("Connect_on_start after sign-out = true, want false: a signed-in person later would be connected without asking")
	}
}

// Test_a_connect_before_sign_in_records_no_wish: that connect fails at
// once, and a wish recorded by it would connect the person at a reboot
// after they sign in, without their ever having connected.
func Test_a_connect_before_sign_in_records_no_wish(t *testing.T) {
	f := new_fixture(t)
	if err := f.app.Connect(context.Background(), connect_params_auto()); err == nil {
		t.Fatal("Connect before sign-in = nil error, want a refusal")
	}
	sign_in(t, f)
	want, err := f.app.manager.Connect_on_start()
	if err != nil {
		t.Fatalf("Connect_on_start: %v", err)
	}
	if want {
		t.Fatal("Connect_on_start after a refused connect = true, want false")
	}
}
