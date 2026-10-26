package evidence

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"singularity-vpn-app/internal/api"
	"testing"
	"time"
)

type fake_client struct {
	now         time.Time
	capture_err error
	send_err    error
	captured    int
	sent        [][]api.Connection_observation
	store       *Store
}

func (f *fake_client) Capture_network_context(ctx context.Context, token string, direct *http.Client) (api.Network_context, error) {
	f.captured++
	if f.capture_err != nil {
		return api.Network_context{}, f.capture_err
	}
	return api.Network_context{Context_id: New_id(), Scope: "asn", Country: "ZZ", Asn: "AS64500", Captured_at: f.now, Expires_at: f.now.Add(24 * time.Hour)}, nil
}
func (f *fake_client) Send_observations(ctx context.Context, token string, events []api.Connection_observation) (api.Observation_ack, error) {
	// Re-entering the store would deadlock if HTTP held its mutex.
	if f.store != nil {
		f.store.Snapshot()
	}
	f.sent = append(f.sent, events)
	if f.send_err != nil {
		return api.Observation_ack{}, f.send_err
	}
	reply := api.Observation_ack{Accepted_ids: []string{}}
	for _, event := range events {
		reply.Accepted_ids = append(reply.Accepted_ids, event.Observation_id)
	}
	return reply, nil
}
func fixture(t *testing.T) (*Collector, *Store, *fake_client, *string, *string) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	client := &fake_client{now: now, store: store}
	token, network := "device-A", "physical-A"
	c := New(Config{Store: store, Client: client, Now: func() time.Time { return client.now }, Auth: func(context.Context) (string, error) { return token, nil }, Capture: func(context.Context) (string, *http.Client, error) { return network, &http.Client{}, nil }})
	return c, store, client, &token, &network
}
func event() api.Connection_observation {
	return api.Connection_observation{Session_id: New_id(), Client_id: "owned-client", Node_id: "node", Protocol: "amneziawg", Mode: "full", Kind: "connection_established", Reason: "verified"}
}
func Test_capture_cache_network_change_and_unknown_isolation(t *testing.T) {
	c, s, client, _, network := fixture(t)
	ctx := context.Background()
	c.Prepare(ctx)
	id := c.Context_id(ctx)
	if id == "" {
		t.Fatal("context missing")
	}
	c.Prepare(ctx)
	if client.captured != 1 {
		t.Fatal("cached physical context recaptured")
	}
	c.Network_changed(ctx)
	if c.Context_id(ctx) != id {
		t.Fatal("same physical network invalidated by notification")
	}
	c.Record(ctx, event())
	*network = "physical-B"
	c.Network_changed(ctx)
	if c.Context_id(ctx) != "" {
		t.Fatal("old context survives network change")
	}
	c.Record(ctx, event())
	c.Invalidate()
	c.Record(ctx, event())
	records, _, _ := s.Snapshot()
	if records[0].Observation.Context_id != id || records[1].Observation.Context_id != "" || records[1].Network == records[2].Network {
		t.Fatalf("context isolation failed: %+v", records)
	}
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(client.sent) != 1 || len(client.sent[0]) != 1 {
		t.Fatal("unknown observations uploaded")
	}
}
func Test_restart_lost_ack_retries_same_ids_and_account_isolation(t *testing.T) {
	c, s, client, token, _ := fixture(t)
	ctx := context.Background()
	c.Prepare(ctx)
	c.Record(ctx, event())
	client.send_err = errors.New("ack lost")
	if c.Flush(ctx) == nil {
		t.Fatal("failed delivery accepted")
	}
	restarted, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	c.config.Store = restarted
	client.send_err = nil
	if err = c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if client.sent[0][0].Observation_id != client.sent[1][0].Observation_id {
		t.Fatal("retry generated another event")
	}
	if err = c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(client.sent) != 2 {
		t.Fatal("acknowledged events resent")
	}
	c.Record(ctx, event())
	*token = "device-B"
	c.Prepare(ctx)
	c.Record(ctx, event())
	if err = c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(client.sent[2]) != 1 {
		t.Fatal("another account's pending events sent")
	}
}
func Test_bounded_atomic_records_ack_priority_and_age(t *testing.T) {
	_, s, client, _, _ := fixture(t)
	for i := 0; i < Max_events+2; i++ {
		o := event()
		o.Observation_id = New_id()
		o.Observed_at = client.now
		o.Context_id = "context"
		if err := s.Add(Record{Account: "a", Observation: o, Acknowledged: i == 0}, client.now); err != nil {
			t.Fatal(err)
		}
	}
	records, _, dropped := s.Snapshot()
	if len(records) != Max_events || dropped != 1 {
		t.Fatalf("size=%d dropped=%d", len(records), dropped)
	}
	reopened, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Pending("a", client.now)) != 32 {
		t.Fatal("restart lost queue")
	}
	if pending := reopened.Pending("a", client.now.Add(Retention+time.Second)); len(pending) != 0 {
		t.Fatal("expired events retained")
	}
}
func Test_old_api_is_optional_and_source_echo_never_becomes_event(t *testing.T) {
	c, s, client, _, _ := fixture(t)
	ctx := context.Background()
	client.capture_err = &api.Error{Status: 404}
	c.Prepare(ctx)
	c.Record(ctx, event())
	if c.Context_id(ctx) != "" {
		t.Fatal("old API produced context")
	}
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(client.sent) != 0 {
		t.Fatal("unscoped event delivered")
	}
	client.capture_err = nil
	c.Prepare(ctx)
	row := api.Connection_evidence{Source: "server", Node_id: "node", Protocol: "amneziawg", Mode: "full", Scope: "asn", Country: "ZZ", Asn: "AS64500", Window_start: client.now.Add(-time.Hour), Window_end: client.now, Observed_at: client.now, Transfer_samples: 1}
	c.Remember(ctx, []api.Connection_evidence{row})
	records, summaries, _ := s.Snapshot()
	if len(records) != 1 || len(summaries) != 1 || summaries[0].Evidence.Source != "server" {
		t.Fatal("server provenance was transformed into local vote")
	}
	row.Region = "unknown-region"
	c.Remember(ctx, []api.Connection_evidence{row})
	_, summaries, _ = s.Snapshot()
	if len(summaries) != 1 {
		t.Fatal("unknown region matched known region")
	}
}
func Test_shutdown_cancels_async_delivery(t *testing.T) {
	c, _, _, _, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("delivery ignored shutdown")
	}
}

func Test_offline_context_preserves_local_network_counters(t *testing.T) {
	c, _, client, _, network := fixture(t)
	ctx := context.Background()
	client.capture_err = &api.Error{Status: 404}
	c.Prepare(ctx)
	c.Record(ctx, event())
	c.Prepare(ctx)
	c.Record(ctx, event())
	rows := c.Local_summaries(ctx)
	if len(rows) != 1 || rows[0].Evidence.Established != 2 {
		t.Fatalf("same physical network split across unknown contexts: %+v", rows)
	}
	*network = "different-physical-network"
	c.Prepare(ctx)
	c.Record(ctx, event())
	rows = c.Local_summaries(ctx)
	if len(rows) != 1 || rows[0].Evidence.Established != 1 {
		t.Fatalf("networks pooled: %+v", rows)
	}
}
func Test_permanent_rejection_retains_local_records_and_unblocks_queue(t *testing.T) {
	c, s, client, _, _ := fixture(t)
	ctx := context.Background()
	c.Prepare(ctx)
	c.Record(ctx, event())
	client.send_err = &api.Error{Status: 403}
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	records, _, _ := s.Snapshot()
	if len(records) != 1 || records[0].Rejected != "http_403" || records[0].Acknowledged {
		t.Fatal("rejection not inspectable")
	}
	client.send_err = nil
	c.Invalidate()
	c.Prepare(ctx)
	c.Record(ctx, event())
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(client.sent) != 2 || len(client.sent[1]) != 1 || client.sent[0][0].Observation_id == client.sent[1][0].Observation_id {
		t.Fatal("poison event blocked new delivery")
	}
	records, _, _ = s.Snapshot()
	if len(records) != 2 || records[0].Rejected == "" || !records[1].Acknowledged {
		t.Fatal("delivery states lost")
	}
}
func Test_local_counters_are_independent_of_echoes_and_ignore_unscored_failures(t *testing.T) {
	c, s, client, token, _ := fixture(t)
	ctx := context.Background()
	c.Prepare(ctx)
	established := event()
	c.Record(ctx, established)
	lost := event()
	lost.Kind = "connection_lost"
	lost.Reason = "reachability_lost"
	c.Record(ctx, lost)
	failed := event()
	failed.Kind = "connection_failed"
	failed.Reason = "local_setup"
	c.Record(ctx, failed)
	ended := event()
	ended.Kind = "session_ended"
	ended.Reason = "manual"
	seconds := 12.0
	ended.Duration_seconds = &seconds
	c.Record(ctx, ended)
	c.Record(ctx, event())
	row := api.Connection_evidence{Source: "app", Node_id: "node", Protocol: "amneziawg", Mode: "full", Scope: "asn", Country: "ZZ", Asn: "AS64500", Window_start: client.now.Add(-time.Hour), Window_end: client.now, Observed_at: client.now, Attempts: 100, Failures: 50}
	c.Remember(ctx, []api.Connection_evidence{row})
	rows := s.Local_summaries(Account(*token), s.Fingerprint("physical-A"), client.now)
	if len(rows) != 1 || rows[0].Evidence.Attempts != 3 || rows[0].Evidence.Established != 2 || rows[0].Evidence.Failures != 1 || rows[0].Evidence.Ended_sessions != 1 || rows[0].Evidence.Active_seconds == nil || *rows[0].Evidence.Active_seconds != 12 {
		t.Fatalf("counters=%+v", rows)
	}
}
