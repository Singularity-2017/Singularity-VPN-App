package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// write_success writes the envelope shape every successful control plane
// response uses, so each test only has to spell out the data.
func write_success(w http.ResponseWriter, data_json string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"success":true,"data":` + data_json + `}`))
}

// no_sleep stands in for the real backoff in every test that is not itself
// testing the backoff: it returns instantly, or the caller context's error
// if that context is already done.
func no_sleep(ctx context.Context, d time.Duration) error {
	return ctx.Err()
}

func Test_new_refuses_an_empty_base_url(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatalf("New accepted an empty base url")
	}
}

func Test_new_refuses_an_unparsable_base_url(t *testing.T) {
	if _, err := New(Config{Base_url: "://not a url"}); err == nil {
		t.Fatalf("New accepted an unparsable base url")
	}
}

func Test_base_url_trailing_slash_produces_the_same_request(t *testing.T) {
	var got_path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got_path = r.URL.Path
		write_success(w, `{"token":"tok"}`)
	}))
	defer server.Close()

	with_slash, err := New(Config{Base_url: server.URL + "/", Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := with_slash.Login(context.Background(), "u", "p"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	path_with_slash := got_path

	without_slash, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := without_slash.Login(context.Background(), "u", "p"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got_path != path_with_slash {
		t.Fatalf("trailing slash changed the request path: %q vs %q", path_with_slash, got_path)
	}
}

func Test_authorization_header_carries_exactly_bearer_token(t *testing.T) {
	var got_header string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got_header = r.Header.Get("Authorization")
		write_success(w, `{"node_id":"n1","node_name":"n","country":"c","protocol":"wg","alternatives":[]}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Recommend(context.Background(), "svpn_abc", Recommend_request{}); err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if got_header != "Bearer svpn_abc" {
		t.Fatalf("Authorization header = %q, want %q", got_header, "Bearer svpn_abc")
	}
}

func Test_login_sends_no_authorization_header(t *testing.T) {
	saw_header := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw_header = r.Header.Get("Authorization") != ""
		write_success(w, `{"token":"tok"}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Login(context.Background(), "u", "p"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if saw_header {
		t.Fatalf("Login sent an Authorization header")
	}
}

func Test_envelope_failure_on_200_becomes_an_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED","message":"unauthorized"}}`))
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Login(context.Background(), "u", "wrong")
	if err == nil {
		t.Fatalf("expected an error")
	}
	if !Is_code(err, Code_unauthorized) {
		t.Fatalf("error does not carry UNAUTHORIZED: %v", err)
	}
}

func Test_non_json_body_with_502_becomes_an_error_carrying_the_status(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Login(context.Background(), "u", "p")
	if err == nil {
		t.Fatalf("expected an error")
	}
	var api_err *Error
	if !errors.As(err, &api_err) {
		t.Fatalf("error is not *Error: %v", err)
	}
	if api_err.Status != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", api_err.Status, http.StatusBadGateway)
	}
}

func Test_transport_failure_is_not_an_error_type(t *testing.T) {
	client, err := New(Config{Base_url: "http://127.0.0.1:1", Retries: 0, Timeout: 200 * time.Millisecond, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Login(context.Background(), "u", "p")
	if err == nil {
		t.Fatalf("expected an error")
	}
	var api_err *Error
	if errors.As(err, &api_err) {
		t.Fatalf("a transport failure came back as *Error: %v", err)
	}
}

func Test_retry_on_500_then_success(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"success":false,"error":{"code":"INTERNAL_ERROR","message":"try again"}}`))
			return
		}
		write_success(w, `{"node_id":"n1","node_name":"n","country":"c","protocol":"wg","alternatives":[]}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Retries: 2, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Recommend(context.Background(), "tok", Recommend_request{}); err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("fake was called %d times, want 2", got)
	}
}

func Test_no_retry_on_400(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"success":false,"error":{"code":"INVALID_INPUT","message":"bad"}}`))
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Retries: 2, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Recommend(context.Background(), "tok", Recommend_request{})
	if err == nil {
		t.Fatalf("expected an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fake was called %d times, want 1", got)
	}
}

func Test_create_profile_is_never_retried(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"success":false,"error":{"code":"INTERNAL_ERROR","message":"lost"}}`))
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Retries: 2, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Create_profile(context.Background(), "tok", "wg", "", 0)
	if err == nil {
		t.Fatalf("expected an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fake was called %d times, want exactly 1 -- a retry here would burn a client_limit slot", got)
	}
}

func Test_cancelled_context_returns_promptly_without_sleeping(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	slept := false
	sleep := func(ctx context.Context, d time.Duration) error {
		slept = true
		return nil
	}

	client, err := New(Config{Base_url: server.URL, Retries: 5, Sleep: sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = client.Recommend(ctx, "tok", Recommend_request{})
	if err == nil {
		t.Fatalf("expected an error")
	}
	if slept {
		t.Fatalf("client slept out a backoff after the caller context was cancelled")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("fake was called %d times, want 0 -- the context was already cancelled", got)
	}
}

func Test_attempt_timeout_derives_from_config(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		write_success(w, `{"token":"tok"}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Timeout: 5 * time.Millisecond, Retries: 0, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Login(context.Background(), "u", "p")
	if err == nil {
		t.Fatalf("expected a timeout error")
	}
}

func Test_mint_device_token_is_never_retried(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"success":false,"error":{"code":"INTERNAL_ERROR","message":"lost"}}`))
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Retries: 2, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Mint_device_token(context.Background(), "tok", "laptop")
	if err == nil {
		t.Fatalf("expected an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fake was called %d times, want exactly 1 -- a retry here leaves a live token nobody holds", got)
	}
}

// The control plane sits behind a reverse proxy that mounts it under a
// path prefix. A base URL carrying that prefix must keep it in every
// request; a request that loses it reaches the proxy's own 404 page.
func Test_a_base_url_path_prefix_is_kept(t *testing.T) {
	var got_path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got_path = r.URL.Path
		write_success(w, `{"token":"tok"}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL + "/vpn", Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Login(context.Background(), "u", "p"); err != nil {
		t.Fatal(err)
	}
	if got_path != "/vpn/api/v1/auth/login" {
		t.Fatalf("request path = %q; want the prefix kept", got_path)
	}
}

// What a proxy answers for a path it does not route: an HTML page. The
// error must say which status and what kind of body came back.
func Test_an_html_page_instead_of_the_api_is_named_as_such(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><head><title>404 Not Found</title></head></html>"))
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Login(context.Background(), "u", "p")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "text/html") {
		t.Fatalf("error = %v; want it to name HTTP 404 and text/html", err)
	}
}

// A control plane older than /vpn/servers lists its nodes at /api/v1/nodes.
// The node's address in that listing is not carried over.
func Test_servers_fall_back_to_the_older_node_listing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/vpn/servers":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"error":{"code":"NOT_FOUND","message":"not found"}}`))
		case "/api/v1/nodes":
			write_success(w, `[{"id":"n-1","ip":"192.0.2.9","name":"Germany","country":"DE","status":"online","city":"Frankfurt"},{"id":"n-2","name":"Old","country":"DE","status":"offline"}]`)
		}
	}))
	defer server.Close()
	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Servers(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !reflect.DeepEqual(got[0], Server{Node_id: "n-1", Name: "Germany", Country: "DE", City: "Frankfurt", Available: true}) || got[1].Available {
		t.Fatalf("servers = %+v", got)
	}
}

// A node's round trip to a game's region comes with the server list, and
// only for the games it has a measurement for.
func Test_servers_carry_the_nodes_game_round_trips(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write_success(w, `{"servers":[{"node_id":"n-1","available":true,"game_rtt_ms":{"pubg":2.2}},{"node_id":"n-2","available":true}]}`)
	}))
	defer server.Close()
	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Servers(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Game_rtt_ms["pubg"] != 2.2 || got[1].Game_rtt_ms != nil {
		t.Fatalf("servers = %+v", got)
	}
}
