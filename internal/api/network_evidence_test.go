package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func Test_network_evidence_wire_and_explicit_direct_transport(t *testing.T) {
	now := time.Now().UTC()
	context_id := "11111111-1111-4111-8111-111111111111"
	observation_id := "22222222-2222-4222-8222-222222222222"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer device" {
			t.Error("missing device identity")
		}
		var data any
		switch r.URL.Path {
		case "/api/v1/network-context":
			if r.Method != "POST" {
				t.Error("wrong context method")
			}
			data = Network_context{Context_id: context_id, Scope: "asn", Asn: "AS64500", Captured_at: now, Expires_at: now.Add(time.Hour)}
		case "/api/v1/vpn/recommend":
			if r.URL.Query().Get("network_context_id") != context_id {
				t.Error("context omitted from recommendation")
			}
			data = Recommendation{Node_id: "node", Protocol: "amneziawg"}
		case "/api/v1/vpn/observations":
			var body struct {
				Observations []Connection_observation `json:"observations"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Observations) != 1 || body.Observations[0].Context_id != context_id {
				t.Error("context missing from observation")
			}
			data = Observation_ack{Accepted_ids: []string{observation_id}, Evidence: []Connection_evidence{{Source: "server", Node_id: "node", Protocol: "amneziawg", Mode: "full", Scope: "asn", Asn: "AS64500", Window_start: now.Add(-time.Hour), Window_end: now, Observed_at: now, Transfer_samples: 1}}}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
	}))
	defer server.Close()
	c, err := New(Config{Base_url: server.URL, Http_client: &http.Client{Transport: reject_transport{}}})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := c.Capture_network_context(context.Background(), "device", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.http_client = server.Client()
	if _, err = c.Recommend(context.Background(), "device", Recommend_request{Network_context_id: captured.Context_id}); err != nil {
		t.Fatal(err)
	}
	ack, err := c.Send_observations(context.Background(), "device", []Connection_observation{{Observation_id: observation_id, Context_id: context_id}})
	if err != nil || len(ack.Evidence) != 1 || ack.Evidence[0].Source != "server" {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
	if requests != 3 {
		t.Fatalf("requests=%d", requests)
	}
}

type reject_transport struct{}

func (reject_transport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("ordinary transport used to capture physical context")
}
func Test_network_evidence_refuses_incomplete_and_unknown_ack(t *testing.T) {
	for _, payload := range []string{`{}`, `{"accepted_ids":["unexpected"]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"data":` + payload + `}`))
		}))
		c, _ := New(Config{Base_url: server.URL})
		if _, err := c.Capture_network_context(context.Background(), "device", server.Client()); err == nil {
			t.Fatal("incomplete context accepted")
		}
		if _, err := c.Send_observations(context.Background(), "device", nil); err == nil {
			t.Fatal("incomplete acknowledgement accepted")
		}
		server.Close()
	}
	for _, status := range []int{404, 405} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		c, _ := New(Config{Base_url: server.URL})
		_, err := c.Capture_network_context(context.Background(), "device", server.Client())
		if !Optional_unavailable(err) {
			t.Fatalf("status%d=%v", status, err)
		}
		server.Close()
	}
}
