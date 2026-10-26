package connection

import (
	"context"
	"errors"
	"net/http"
	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/evidence"
	"testing"
	"time"
)

type evidence_client struct{ now func() time.Time }

func (c evidence_client) Capture_network_context(context.Context, string, *http.Client) (api.Network_context, error) {
	now := c.now()
	return api.Network_context{Context_id: evidence.New_id(), Scope: "asn", Country: "ZZ", Asn: "AS64500", Captured_at: now, Expires_at: now.Add(time.Hour)}, nil
}
func (c evidence_client) Send_observations(context.Context, string, []api.Connection_observation) (api.Observation_ack, error) {
	panic("lifecycle must never deliver HTTP while holding manager lock")
}
func evidence_manager(t *testing.T) (*test_env, *Manager, *evidence.Store) {
	t.Helper()
	env := new_test_env(t)
	env.store.state.Client_id = "client-1"
	m := env.build()
	store, err := evidence.Open("")
	if err != nil {
		t.Fatal(err)
	}
	m.evidence = evidence.New(evidence.Config{Store: store, Client: evidence_client{now: m.now}, Now: m.now, Auth: func(context.Context) (string, error) { return "test-device", nil }, Capture: func(context.Context) (string, *http.Client, error) {
		if !m.op_mu.TryLock() {
			t.Error("physical capture ran while connection lock held")
		} else {
			m.op_mu.Unlock()
		}
		return "physical-network", &http.Client{}, nil
	}})
	return env, m, store
}
func Test_lifecycle_records_verified_success_and_manual_end_without_failure(t *testing.T) {
	env, m, store := evidence_manager(t)
	ctx := context.Background()
	if err := m.Connect(ctx, Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatal(err)
	}
	records, _, _ := store.Snapshot()
	if len(records) != 1 || records[0].Observation.Kind != "connection_established" || records[0].Observation.Context_id == "" {
		t.Fatalf("records=%+v", records)
	}
	env.clock.advance(12 * time.Second)
	if err := m.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	records, _, _ = store.Snapshot()
	if len(records) != 2 || records[1].Observation.Kind != "session_ended" || records[1].Observation.Reason != "manual" || records[1].Observation.Duration_seconds == nil || *records[1].Observation.Duration_seconds != 12 {
		t.Fatalf("records=%+v", records)
	}
	rows := m.evidence.Local_summaries(ctx)
	if len(rows) != 1 || rows[0].Evidence.Failures != 0 || rows[0].Evidence.Attempts != 1 {
		t.Fatalf("counters=%+v", rows)
	}
}
func Test_local_setup_and_cancel_never_become_scored_transport_failures(t *testing.T) {
	env, m, store := evidence_manager(t)
	env.protocol.connect_err = errors.New("adapter unavailable")
	if err := m.Connect(context.Background(), Connect_request{Mode: Mode_manual, Node_id: "de-01", Protocol: "wireguard"}); err == nil {
		t.Fatal("fake failure missing")
	}
	records, _, _ := store.Snapshot()
	if len(records) != 1 || records[0].Observation.Reason != "local_setup" {
		t.Fatalf("records=%+v", records)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.failed_observation(ctx, m.current, context.Canceled, false)
	records, _, _ = store.Snapshot()
	if len(records) != 1 {
		t.Fatal("cancellation produced network failure")
	}
	if rows := m.evidence.Local_summaries(context.Background()); len(rows) != 1 || rows[0].Evidence.Failures != 0 {
		t.Fatalf("local setup scored: %+v", rows)
	}
}
func Test_probe_blip_is_not_lifecycle_failure_confirmed_loss_is_once(t *testing.T) {
	env, m, store := evidence_manager(t)
	ctx := context.Background()
	if err := m.Connect(ctx, Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatal(err)
	}
	// Physical refresh is local route inspection, not HTTP; it is permitted in loss handling.
	m.evidence = evidence.New(evidence.Config{Store: store, Client: evidence_client{now: m.now}, Now: m.now, Auth: func(context.Context) (string, error) { return "test-device", nil }, Capture: func(context.Context) (string, *http.Client, error) { return "physical-network", &http.Client{}, nil }})
	m.evidence.Prepare(ctx)
	env.prober.set([]prober_response{{err: diagnostics.Err_no_internet}, {err: diagnostics.Err_no_internet}, {sample: healthy_sample}})
	m.Tick(ctx, env.clock.advance(5*time.Second))
	records, _, _ := store.Snapshot()
	if len(records) != 1 {
		t.Fatal("single health probe became lifecycle failure")
	}
	m.Tick(ctx, env.clock.advance(5*time.Second))
	records, _, _ = store.Snapshot()
	counts := map[string]int{}
	for _, record := range records {
		counts[record.Observation.Kind]++
	}
	if counts["connection_lost"] != 1 || counts["session_ended"] != 1 || counts["connection_established"] != 2 {
		t.Fatalf("lifecycle=%v", counts)
	}
	rows := m.evidence.Local_summaries(ctx)
	if len(rows) != 1 || rows[0].Evidence.Failures != 1 || rows[0].Evidence.Established != 2 {
		t.Fatalf("recovery erased loss: %+v", rows)
	}
}
