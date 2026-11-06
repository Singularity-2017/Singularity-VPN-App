package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func Test_vless_export_is_authenticated_gated_and_explicit(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "available"}[available], func(t *testing.T) {
			downloads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer token" || r.URL.Query().Get("client_id") != "profile-1" {
					t.Error("missing authentication or profile")
				}
				if r.URL.Path == "/api/v1/vpn/vless/options" {
					if available {
						write_success(w, `{"nodes":[{"node_id":"node-1","vless_available":true}]}`)
					} else {
						write_success(w, `{"nodes":[{"node_id":"node-1","vless_available":false}]}`)
					}
					return
				}
				downloads++
				if r.Method != "GET" || r.URL.Path != "/api/v1/vpn/config" || r.URL.Query().Get("protocol") != "vless" || r.URL.Query().Get("mode") != "vpn" || r.URL.Query().Get("node_id") != "node-1" {
					t.Error("incorrect export contract")
				}
				write_success(w, `{"config":"vless://test@example.invalid:443?security=reality","protocol":"vless","mode":"vpn","expires_at":"2099-01-01T00:00:00Z"}`)
			}))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL, Sleep: no_sleep})
			got, err := client.Fetch_vless(context.Background(), "token", "profile-1", "node-1")
			if available && (err != nil || got.Config == "") {
				t.Fatalf("export failed: %v", err)
			}
			if !available && (err == nil || downloads != 0) {
				t.Fatal("unavailable node was exported")
			}
		})
	}
}

func Test_vless_export_rejects_substitution_and_malformed_uri(t *testing.T) {
	for _, body := range []string{
		`{"config":"vless://test@example.invalid:443","protocol":"wireguard","mode":"vpn","expires_at":"2099-01-01T00:00:00Z"}`,
		`{"config":"vless://test@example.invalid:443","protocol":"vless","mode":"antizapret","expires_at":"2099-01-01T00:00:00Z"}`,
		`{"config":"vless://test@example.invalid:443\nsecret","protocol":"vless","mode":"vpn","expires_at":"2099-01-01T00:00:00Z"}`,
		`{"config":"vless://","protocol":"vless","mode":"vpn","expires_at":"2099-01-01T00:00:00Z"}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/vpn/vless/options" {
				write_success(w, `{"nodes":[{"node_id":"n","vless_available":true}]}`)
				return
			}
			write_success(w, body)
		}))
		client, _ := New(Config{Base_url: server.URL, Sleep: no_sleep})
		if _, err := client.Fetch_vless(context.Background(), "token", "p", "n"); err == nil {
			t.Error("unsafe configuration accepted")
		}
		server.Close()
	}
}
