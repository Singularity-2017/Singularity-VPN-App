package connection

import (
	"context"
	"sync"
	"testing"
	"time"

	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/events"
)

func Test_degradation_snapshot_and_event_carry_the_actual_cause(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatal(err)
	}
	channel, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()
	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Rtt: 20 * time.Millisecond, Packet_loss: .25}}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second)) // a blip; the next bad sample confirms it
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	detail := manager.Degradation()
	if !detail.Failed_probes || detail.High_latency || detail.Packet_loss != .25 {
		t.Fatalf("wrong explanation: %+v", detail)
	}
	found := false
	for _, event := range drain_events(channel) {
		if event.Kind != events.Kind_connection_degraded {
			continue
		}
		found = true
		if event.Data != detail {
			t.Fatalf("event lost the measurement: %+v", event.Data)
		}
	}
	if !found {
		t.Fatal("degradation event missing")
	}
	env.prober.set([]prober_response{{sample: healthy_sample}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	if manager.State() != State_connected || manager.Degradation().Failed_probes {
		t.Fatal("recovery retained the old cause")
	}
	if err := manager.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.Degradation() != (diagnostics.Degradation{}) {
		t.Fatal("new session retained the old measurement")
	}
}

func Test_status_snapshot_never_mixes_two_probe_results(t *testing.T) {
	manager := new_test_env(t).build()
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for range 2000 {
			manager.set_sample(diagnostics.Sample{Rtt: 100 * time.Millisecond, Packet_loss: .25}, diagnostics.Degradation{Rtt: 100 * time.Millisecond, Packet_loss: .25, Failed_probes: true})
			manager.set_sample(diagnostics.Sample{Rtt: 20 * time.Millisecond}, diagnostics.Degradation{Rtt: 20 * time.Millisecond})
		}
	}()
	for range 2000 {
		value := manager.Status_snapshot()
		if value.Sample.Rtt != value.Degradation.Rtt || value.Sample.Packet_loss != value.Degradation.Packet_loss {
			t.Errorf("snapshot mixed measurement and diagnosis: %+v", value)
			break
		}
	}
	writers.Wait()
}

func Test_one_bad_sample_is_a_blip_until_the_recheck_confirms_it(t *testing.T) {
	env := new_test_env(t)
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatal(err)
	}
	channel, unsubscribe := env.bus.Subscribe()
	defer unsubscribe()
	bad := diagnostics.Sample{Tunnel_up: true, Internet_ok: true, Rtt: 20 * time.Millisecond, Packet_loss: .25}

	env.prober.set([]prober_response{{sample: bad}, {sample: healthy_sample}, {sample: bad}, {sample: bad}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	if manager.State() != State_connected {
		t.Fatalf("one bad sample: State() = %v, want connected", manager.State())
	}
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	kinds := kinds_of(drain_events(channel))
	if !kind_list_contains(kinds, events.Kind_connection_blip) || kind_list_contains(kinds, events.Kind_connection_degraded) {
		t.Fatalf("a bad sample the re-check cleared: events %v, want a blip and no degradation", kinds)
	}

	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))
	if manager.State() != State_degraded || !kind_list_contains(kinds_of(drain_events(channel)), events.Kind_connection_degraded) {
		t.Fatalf("two bad samples in a row: State() = %v, want degraded with its event", manager.State())
	}
}
