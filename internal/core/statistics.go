package core

import (
	"context"
	"time"

	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/statistics"
)

func open_statistics(path string, now func() time.Time) (*statistics.Store, error) {
	if path == "" {
		return nil, nil
	}
	return statistics.Open(path, now)
}

// These synchronous observers only update memory. Unlike the UI event bus,
// they cannot drop a transition when a subscriber's buffer is full.
func statistics_observer(store *statistics.Store) func(events.Event) {
	if store == nil {
		return nil
	}
	return func(event events.Event) {
		switch event.Kind {
		case events.Kind_state_changed:
			if change, ok := event.Data.(connection.State_change); ok {
				store.Transition(string(change.From), string(change.To), event.At)
			}
		case events.Kind_connection_lost:
			store.Lost()
		case events.Kind_switch_completed:
			store.Switched()
		case events.Kind_connection_blip:
			store.Blip()
		case events.Kind_connection_degraded:
			store.Degraded()
		}
	}
}

func statistics_sample_observer(store *statistics.Store) func(diagnostics.Sample) {
	if store == nil {
		return nil
	}
	return func(sample diagnostics.Sample) {
		store.Observe_sample(sample.Taken_at, sample.Rtt, sample.Packet_loss)
		if sample.Udp_measured {
			store.Observe_udp(sample.Taken_at, sample.Udp_rtt, sample.Udp_loss)
		}
	}
}

func (a *Application) save_statistics() {
	a.statistics_save_mu.Lock()
	defer a.statistics_save_mu.Unlock()
	err := a.statistics.Save()
	was_failed := a.statistics_error.Swap(err != nil)
	if err != nil && !was_failed {
		a.logger.Warn("local statistics checkpoint failed")
	}
}

func (a *Application) checkpoint_statistics(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.statistics_stop:
			return
		case <-ctx.Done():
			return // Stop checkpoints after the final connection transition.
		case <-ticker.C:
			a.save_statistics()
		}
	}
}
