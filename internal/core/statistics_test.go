package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/security"
)

func Test_statistics_receive_transitions_even_when_event_bus_is_full(t *testing.T) {
	d := base_dependencies()
	defer d.Bus.Close()
	_, unsubscribe := d.Bus.Subscribe() // deliberately never drained
	defer unsubscribe()
	now := time.Now()
	d.Now = func() time.Time { return now }
	d.Statistics_path = filepath.Join(t.TempDir(), "stats.json")
	d.Control_plane.(*fake_control_plane).recommendation.Node_id = "test-node"
	d.Control_plane.(*fake_control_plane).recommendation.Protocol = "wireguard"
	d.Prober.(*fake_prober).sample.Tunnel_up = true
	d.Prober.(*fake_prober).sample.Internet_ok = true
	d.Prober.(*fake_prober).sample.Dns_ok = true
	d.Prober.(*fake_prober).sample.Rtt = time.Millisecond
	if err := d.Secrets.Set(security.Key_device_token, []byte("test-token")); err != nil {
		t.Fatal(err)
	}
	a, err := New_application(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.manager.Connect(context.Background(), connection.Connect_request{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := a.manager.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := a.Status(context.Background())
	if err != nil || status.Statistics == nil || status.Statistics.Connections != 1 || status.Statistics.Total_connected_seconds != 60 {
		t.Fatalf("statistics missing: %+v, %v", status.Statistics, err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	b, err := New_application(d)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := b.Status(context.Background())
	if err != nil || restored.Statistics == nil || restored.Statistics.Total_connected_seconds != 60 {
		t.Fatal("shutdown checkpoint was not restored")
	}
}

func Test_statistics_failure_is_visible_without_blocking_the_application(t *testing.T) {
	d := base_dependencies()
	defer d.Bus.Close()
	d.Statistics_path = filepath.Join(t.TempDir(), "stats.json")
	if err := os.WriteFile(d.Statistics_path, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New_application(d)
	if err != nil {
		t.Fatal(err)
	}
	status, err := a.Status(context.Background())
	if err != nil || status.Statistics != nil || !status.Statistics_error {
		t.Fatalf("missing storage error: %+v, %v", status, err)
	}
}

func Test_analysis_cache_restores_only_as_historical_evidence(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	f.app.analysis_cache_path = filepath.Join(t.TempDir(), "analysis.json")
	original, err := f.app.Analyze(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.app.analysis = analysis_cache{}
	last, err := f.app.Analysis(context.Background())
	if err != nil || !last.Stale || last.Result == nil || len(last.Result.Checks) != len(original.Checks) {
		t.Fatalf("missing historical result: %+v, %v", last, err)
	}
	if _, err := f.app.Analyze(context.Background()); err != nil {
		t.Fatal(err)
	}
	last, _ = f.app.Analysis(context.Background())
	if last.Stale || last.Result == nil {
		t.Fatal("fresh measurement remained stale")
	}
	// Repeated replacement must work on Windows too.
	f.app.analysis = analysis_cache{}
	last, _ = f.app.Analysis(context.Background())
	if !last.Stale || last.Result == nil {
		t.Fatal("replacement was not persisted")
	}
}

func Test_invalid_analysis_cache_is_not_shown(t *testing.T) {
	f := new_fixture(t)
	f.app.analysis_cache_path = filepath.Join(t.TempDir(), "analysis.json")
	if err := os.WriteFile(f.app.analysis_cache_path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := f.app.Analysis(context.Background())
	if err != nil || got.Result != nil {
		t.Fatalf("invalid result shown: %+v, %v", got, err)
	}
}

func Test_network_change_discards_an_inflight_analysis(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	answer := start_blocked_analysis(t, f)
	f.app.Network_changed(context.Background())
	close(f.reach_prober.release)
	if err := await_answer(t, answer); err == nil {
		t.Fatal("accepted a result spanning two networks")
	}
	last, _ := f.app.Analysis(context.Background())
	if last.Present {
		t.Fatal("mixed-network result was cached")
	}
}
