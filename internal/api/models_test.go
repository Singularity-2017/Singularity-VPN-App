package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func Test_login_returns_the_token(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/login" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if body.Username != "alice" || body.Password != "secret" {
			t.Fatalf("unexpected credentials: %+v", body)
		}
		write_success(w, `{"token":"session-token-1"}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	token, err := client.Login(context.Background(), "alice", "secret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token != "session-token-1" {
		t.Fatalf("token = %q, want %q", token, "session-token-1")
	}
}

func Test_login_wrong_password_is_unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED","message":"unauthorized"}}`))
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Login(context.Background(), "alice", "wrong")
	if !Is_code(err, Code_unauthorized) {
		t.Fatalf("expected UNAUTHORIZED, got %v", err)
	}
}

func Test_mint_device_token_returns_the_device(t *testing.T) {
	expires := "2026-10-01T00:00:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/device" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		write_success(w, `{"device_id":"dev-1","device_token":"svpn_abc","expires_at":"`+expires+`"}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	device, err := client.Mint_device_token(context.Background(), "session-token", "my-desktop")
	if err != nil {
		t.Fatalf("Mint_device_token: %v", err)
	}
	want_expires, _ := time.Parse(time.RFC3339, expires)
	if device.Device_id != "dev-1" || device.Device_token != "svpn_abc" || !device.Expires_at.Equal(want_expires) {
		t.Fatalf("unexpected device: %+v", device)
	}
}

func Test_mint_device_token_surfaces_device_token_cannot_mint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success":false,"error":{"code":"DEVICE_TOKEN_CANNOT_MINT","message":"a device token cannot mint another"}}`))
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Mint_device_token(context.Background(), "svpn_device_token", "my-desktop")
	if !Is_code(err, Code_device_token_cannot_mint) {
		t.Fatalf("expected DEVICE_TOKEN_CANNOT_MINT, got %v", err)
	}
}

func Test_recommend_does_not_invent_region_or_city(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/vpn/recommend" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		write_success(w, `{"node_id":"n1","node_name":"Node One","country":"NL","protocol":"wireguard","alternatives":[{"node":"n2","protocol":"openvpn","state":"available"}]}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec, err := client.Recommend(context.Background(), "tok")
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if rec.Region != "" || rec.City != "" {
		t.Fatalf("recommendation invented region/city: %+v", rec)
	}
	if rec.Node_id != "n1" || rec.Node_name != "Node One" || rec.Country != "NL" || rec.Protocol != "wireguard" {
		t.Fatalf("unexpected recommendation: %+v", rec)
	}
	if len(rec.Alternatives) != 1 || rec.Alternatives[0].Node != "n2" {
		t.Fatalf("unexpected alternatives: %+v", rec.Alternatives)
	}
}

func Test_create_profile_omits_empty_fields_and_returns_the_profile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if _, present := raw["protocol"]; present {
			t.Fatalf("request sent an empty protocol: %+v", raw)
		}
		if _, present := raw["node_id"]; present {
			t.Fatalf("request sent an empty node_id: %+v", raw)
		}
		write_success(w, `{"client_id":"c1","config":"cfg","node":"n1","node_ip":"10.0.0.1","protocol":"wireguard","expires_at":"2026-10-01T00:00:00Z","alternatives":[]}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	profile, err := client.Create_profile(context.Background(), "tok", "", "", 0)
	if err != nil {
		t.Fatalf("Create_profile: %v", err)
	}
	if profile.Client_id != "c1" || profile.Node_id != "n1" || profile.Node_ip != "10.0.0.1" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
}

func Test_fetch_config_omits_empty_query_parameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("client_id") != "c1" {
			t.Fatalf("client_id = %q, want %q", r.URL.Query().Get("client_id"), "c1")
		}
		if _, present := r.URL.Query()["protocol"]; present {
			t.Fatalf("request sent an empty protocol parameter")
		}
		if _, present := r.URL.Query()["node_id"]; present {
			t.Fatalf("request sent an empty node_id parameter")
		}
		write_success(w, `{"config":"cfg","protocol":"wireguard","mode":"full","expires_at":"2026-10-01T00:00:00Z"}`)
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cfg, err := client.Fetch_config(context.Background(), "tok", "c1", "", "")
	if err != nil {
		t.Fatalf("Fetch_config: %v", err)
	}
	if cfg.Config != "cfg" || cfg.Mode != "full" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func Test_fetch_config_client_not_found(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"success":false,"error":{"code":"CLIENT_NOT_FOUND","message":"no such client"}}`))
	}))
	defer server.Close()

	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Fetch_config(context.Background(), "tok", "gone", "", "")
	if !Is_code(err, Code_client_not_found) {
		t.Fatalf("expected CLIENT_NOT_FOUND, got %v", err)
	}
}
