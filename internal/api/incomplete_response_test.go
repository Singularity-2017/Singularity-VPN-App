package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// serve_data answers every request with the given data object inside a
// successful envelope, so a table below can say "this is the body" and
// nothing else.
func serve_data(t *testing.T, data string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write_success(w, data)
	}))
	t.Cleanup(server.Close)

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// call_by_name runs one control-plane method and returns only its error, so
// the table can cover all five without five near-identical helpers.
func call_by_name(t *testing.T, client *Client, method string) error {
	t.Helper()
	ctx := context.Background()
	switch method {
	case "Login":
		_, err := client.Login(ctx, "alice", "secret")
		return err
	case "Mint_device_token":
		_, err := client.Mint_device_token(ctx, "session", "laptop")
		return err
	case "Recommend":
		_, err := client.Recommend(ctx, "token", Recommend_request{})
		return err
	case "Create_profile":
		_, err := client.Create_profile(ctx, "token", "wireguard", "node-1", 30)
		return err
	case "Fetch_config":
		_, err := client.Fetch_config(ctx, "token", "client-1", "wireguard", "node-1", "")
		return err
	}
	t.Fatalf("no such method: %s", method)
	return nil
}

const an_expiry = `"expires_at":"2026-10-01T00:00:00Z"`

// The whole point of the guard: every field this client acts on, dropped one
// at a time. A field the control plane renames arrives exactly like this --
// absent -- and decoding it into a zero value would let the call succeed
// carrying nothing.
func Test_a_response_missing_a_field_this_client_acts_on_is_refused(t *testing.T) {
	cases := []struct {
		method string
		data   string
		want   string
	}{
		{"Login", `{}`, "token"},

		{"Mint_device_token", `{"device_token":"svpn_a",` + an_expiry + `}`, "device_id"},
		{"Mint_device_token", `{"device_id":"dev-1",` + an_expiry + `}`, "device_token"},
		{"Mint_device_token", `{"device_id":"dev-1","device_token":"svpn_a"}`, "expires_at"},

		{"Recommend", `{"protocol":"wireguard"}`, "node_id"},
		{"Recommend", `{"node_id":"node-1"}`, "protocol"},

		{"Create_profile", `{"config":"c","node":"node-1","node_ip":"198.51.100.4","protocol":"wireguard",` + an_expiry + `}`, "client_id"},
		{"Create_profile", `{"client_id":"cl-1","node":"node-1","node_ip":"198.51.100.4","protocol":"wireguard",` + an_expiry + `}`, "config"},
		{"Create_profile", `{"client_id":"cl-1","config":"c","node_ip":"198.51.100.4","protocol":"wireguard",` + an_expiry + `}`, "node"},
		{"Create_profile", `{"client_id":"cl-1","config":"c","node":"node-1","protocol":"wireguard",` + an_expiry + `}`, "node_ip"},
		{"Create_profile", `{"client_id":"cl-1","config":"c","node":"node-1","node_ip":"198.51.100.4",` + an_expiry + `}`, "protocol"},
		{"Create_profile", `{"client_id":"cl-1","config":"c","node":"node-1","node_ip":"198.51.100.4","protocol":"wireguard"}`, "expires_at"},

		{"Fetch_config", `{"protocol":"wireguard","mode":"vpn",` + an_expiry + `}`, "config"},
		{"Fetch_config", `{"config":"c","mode":"vpn",` + an_expiry + `}`, "protocol"},
		{"Fetch_config", `{"config":"c","protocol":"wireguard",` + an_expiry + `}`, "mode"},
		{"Fetch_config", `{"config":"c","protocol":"wireguard","mode":"vpn"}`, "expires_at"},
	}

	for _, c := range cases {
		err := call_by_name(t, serve_data(t, c.data), c.method)
		if !errors.Is(err, Err_incomplete_response) {
			t.Fatalf("%s without %s: err = %v, want Err_incomplete_response", c.method, c.want, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s without %s: err = %q, which does not name the field", c.method, c.want, err)
		}
	}
}

// Every missing field at once, because a rename on the server usually moves
// more than one and learning them one round trip at a time is slow.
func Test_an_incomplete_response_names_every_field_it_lacks(t *testing.T) {
	err := call_by_name(t, serve_data(t, `{"client_id":"cl-1","config":"c"}`), "Create_profile")
	for _, field := range []string{"node", "node_ip", "protocol", "expires_at"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("err = %q, which does not name %s", err, field)
		}
	}
}

// Stage 2 branches on which of the three things went wrong, so an incomplete
// answer must not look like either of the other two: the server did not say
// no, and the connection worked.
func Test_an_incomplete_response_is_not_the_server_saying_no(t *testing.T) {
	err := call_by_name(t, serve_data(t, `{}`), "Recommend")

	var api_err *Error
	if errors.As(err, &api_err) {
		t.Fatalf("an incomplete answer came back as *Error %+v", api_err)
	}
	if Is_code(err, Code_internal_error) || Is_code(err, Code_invalid_input) {
		t.Fatalf("an incomplete answer came back carrying a control-plane code: %v", err)
	}
}

// A refused answer hands back nothing rather than the half of it that did
// arrive: a caller that ignores the error must not find a usable-looking
// profile in its hands.
func Test_a_refused_answer_carries_no_half_filled_value(t *testing.T) {
	profile, err := serve_data(t, `{"client_id":"cl-1","config":"c"}`).
		Create_profile(context.Background(), "token", "wireguard", "node-1", 30)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !reflect.DeepEqual(profile, Profile{}) {
		t.Fatalf("profile = %+v, want the zero value", profile)
	}
}

// The line drawn deliberately: region and city are omitted by the server
// when nobody has typed them, and node_name and country are shown to a
// person rather than acted on. None of them may turn a usable recommendation
// into a refusal to connect -- a rename there is the contract test's job, in
// the control plane repository.
func Test_a_recommendation_without_a_place_is_still_usable(t *testing.T) {
	client := serve_data(t, `{"node_id":"node-1","protocol":"wireguard","alternatives":[]}`)
	recommendation, err := client.Recommend(context.Background(), "token", Recommend_request{})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if recommendation.Node_id != "node-1" || recommendation.Protocol != "wireguard" {
		t.Fatalf("recommendation = %+v", recommendation)
	}
	if recommendation.Region != "" || recommendation.City != "" {
		t.Fatalf("decoding invented a place: %+v", recommendation)
	}
}
