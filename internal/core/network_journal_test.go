package core

import (
	"context"
	"testing"

	"singularity-vpn-app/internal/events"
)

func Test_network_change_emits_one_identifier_free_event(t *testing.T) {
	for _, scenario := range []string{"connected", "disconnected", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			f := new_fixture(t)
			if scenario == "connected" {
				sign_in(t, f)
				connect(t, f)
			}
			channel, unsubscribe := f.bus.Subscribe()
			defer unsubscribe()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			f.app.Network_changed(ctx)
			count := 0
			for len(channel) > 0 {
				event := <-channel
				if event.Kind == events.Kind_network_changed {
					count++
					if event.Data != nil || event.Session_id != "" || event.At.IsZero() {
						t.Fatalf("network event carries identifiers or lacks time: %+v", event)
					}
				}
			}
			want := 1
			if scenario == "cancelled" {
				want = 0
			}
			if count != want {
				t.Fatalf("network change events = %d, want %d", count, want)
			}
		})
	}
}
