package ipc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Test_a_password_is_not_in_the_printed_params guards Sign_in_params.String:
// one careless %v on this value in a log line later must not be able to
// leak the credential it carries.
//
// Mutation check performed: with Sign_in_params.String deleted from
// protocol.go, this test failed with
//
//	got %v = "{Alice hunter2}", want it not to contain the password
//
// (fmt falls back to the struct's default representation, which prints
// every field including Password). Restoring String() made it pass again.
func Test_a_password_is_not_in_the_printed_params(t *testing.T) {
	params := Sign_in_params{Username: "alice", Password: "hunter2"}

	if got := fmt.Sprintf("%v", params); strings.Contains(got, "hunter2") {
		t.Fatalf("got %%v = %q, want it not to contain the password", got)
	}
	if got := fmt.Sprintf("%+v", params); strings.Contains(got, "hunter2") {
		t.Fatalf("got %%+v = %q, want it not to contain the password", got)
	}
}

// Test_a_sign_in_result_carries_no_token is structural rather than
// behavioural on purpose: it reflects over Sign_in_result's fields so that
// adding a device_token field later trips it, instead of relying on nobody
// noticing that it was added.
func Test_a_sign_in_result_carries_no_token(t *testing.T) {
	typ := reflect.TypeOf(Sign_in_result{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if strings.Contains(strings.ToLower(name), "token") {
			t.Fatalf("Sign_in_result has field %q, want no field naming a token", name)
		}
	}
}

// Test_a_status_with_no_connection_omits_since checks the pointer-vs-value
// choice on Status_result.Since: a disconnected status has no connection
// time to report, and the project rule is that a number shown to a person
// is either a real measurement or absent -- never a placeholder standing
// for "unknown".
//
// Mutation check performed: with Since changed to a bare time.Time (still
// tagged omitempty), this test failed with
//
//	marshalled status = `{"state":"disconnected","signed_in":false,"since":"0001-01-01T00:00:00Z"}`, want it not to contain "since"
//
// because encoding/json does not consider a zero time.Time to be empty, so
// omitempty does not drop it -- only a nil pointer does. Restoring Since to
// *time.Time made it pass again.
func Test_a_status_with_no_connection_omits_since(t *testing.T) {
	status := Status_result{State: "disconnected", Signed_in: false}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal Status_result: %v", err)
	}
	if strings.Contains(string(data), "since") {
		t.Fatalf("marshalled status = %s, want it not to contain %q", data, "since")
	}
}

// Test_a_status_with_a_connection_carries_since is the other half of the
// pointer check: a real time must actually appear once there is one to
// report.
func Test_a_status_with_a_connection_carries_since(t *testing.T) {
	since := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	status := Status_result{State: "connected", Signed_in: true, Since: &since}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal Status_result: %v", err)
	}
	if !strings.Contains(string(data), "2026-09-22T12:00:00Z") {
		t.Fatalf("marshalled status = %s, want it to contain the Since timestamp", data)
	}
}

func Test_connect_params_refuse_a_missing_mode(t *testing.T) {
	err := Connect_params{}.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming the missing mode")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("Validate() error = %q, want it to name the mode field", err)
	}
}

func Test_manual_connect_params_refuse_a_missing_node(t *testing.T) {
	err := Connect_params{Mode: Mode_manual, Protocol: "wireguard"}.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming the missing node_id")
	}
	if !strings.Contains(err.Error(), "node_id") {
		t.Fatalf("Validate() error = %q, want it to name node_id", err)
	}

	err = Connect_params{Mode: Mode_manual, Node_id: "node-1"}.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming the missing protocol")
	}
	if !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("Validate() error = %q, want it to name protocol", err)
	}
}

// Test_auto_connect_params_ignore_a_stray_node_and_protocol documents the
// deliberate asymmetry in Validate: Mode_auto never reads Node_id or
// Protocol, so a caller that sent them anyway is not refused for it.
func Test_auto_connect_params_ignore_a_stray_node_and_protocol(t *testing.T) {
	err := Connect_params{Mode: Mode_auto, Node_id: "node-1", Protocol: "wireguard"}.Validate()
	if err != nil {
		t.Fatalf("Validate() = %v, want auto mode to ignore node_id and protocol rather than refuse them", err)
	}
}

func Test_manual_connect_params_with_everything_pass(t *testing.T) {
	err := Connect_params{Mode: Mode_manual, Node_id: "node-1", Protocol: "wireguard"}.Validate()
	if err != nil {
		t.Fatalf("Validate() = %v, want a complete manual request to pass", err)
	}
}

func Test_sign_in_params_refuse_an_empty_username_or_password(t *testing.T) {
	if err := (Sign_in_params{Password: "x"}).Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error naming the missing username")
	}
	if err := (Sign_in_params{Username: "alice"}).Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error naming the missing password")
	}
	if err := (Sign_in_params{Username: "alice", Password: "x"}).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want a complete sign_in request to pass", err)
	}
}

func Test_a_match_server_answer_is_one_of_three_about_a_real_server(t *testing.T) {
	good := []Match_server_answer{
		{Game: "pubg", Server: "203.0.113.10:7086", Answer: "yes"},
		{Game: "pubg", Server: "203.0.113.10:7086", Answer: "no"},
		{Game: "pubg", Server: "203.0.113.10:7086", Answer: "no", Shown: "203.0.113.99"},
		{Game: "pubg", Server: "203.0.113.10:7086", Answer: "no", Shown: "203.0.113.99:7086"},
		{Game: "pubg", Server: "203.0.113.10:7086", Answer: "dismiss"},
	}
	for _, a := range good {
		if err := a.Validate(); err != nil {
			t.Errorf("%+v refused: %v", a, err)
		}
	}
	bad := []Match_server_answer{
		{Game: "PUBG", Server: "203.0.113.10:7086", Answer: "yes"},
		{Game: "pubg", Server: "203.0.113.10", Answer: "yes"},
		{Game: "pubg", Server: "203.0.113.10:7086", Answer: "maybe"},
		{Game: "pubg", Server: "203.0.113.10:7086", Answer: "no", Shown: "not an address"},
	}
	for _, a := range bad {
		if err := a.Validate(); err == nil {
			t.Errorf("%+v accepted", a)
		}
	}
}
