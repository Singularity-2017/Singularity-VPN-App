package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/reach"
)

// analysis_reuse is how long a finished analysis answers a new request: a
// run within this long of the last successful one returns it instead of
// probing again, so a window opened twice in a minute does not probe twice.
const analysis_reuse = 60 * time.Second

// analysis_network_spacing is the least time between two runs started by a
// network change, so a flapping link does not turn into a probe storm.
const analysis_network_spacing = 5 * time.Minute

// network_change_after_disconnect is how long after the connection went
// down a network change is taken for the tunnel's own adapter going away
// rather than the network under the machine changing. The adapter, once
// deleted, can no longer be recognised by name in the notification.
const network_change_after_disconnect = 15 * time.Second

// err_analysis_spaced says a network-change run was held back by the spacing.
var err_analysis_spaced = errors.New("core: the last analysis started too recently")

// analysis_run_timeout bounds a whole run -- the catalog fetch and the
// probes -- on top of the bounds each of them has.
const analysis_run_timeout = 45 * time.Second

// analysis_flight is one run in progress. Every caller that arrives while it
// runs waits on done and gets the same answer.
type analysis_flight struct {
	done   chan struct{}
	result ipc.Analysis_result
	err    error
}

// analysis_cache is the last successful result, in memory only, and the run
// in progress if there is one.
type analysis_cache struct {
	mu     sync.Mutex
	last   ipc.Analysis_result
	has    bool
	flight *analysis_flight

	// last_started is when the most recent run began, successful or not.
	// It spaces the runs a network change asks for.
	last_started time.Time
}

// Analyze answers Command_analyze: it probes the catalog directly and says
// what the network under the device blocks.
//
// The tunnel has to be down, and has to stay down for the whole run: probes
// that went through our own node would measure the node and call it the ISP.
// So the manager's transition counter is read before anything else and again
// when the probes are done, and a result is discarded -- as tunnel_up -- if
// the counter moved, however briefly the state changed.
func (a *Application) Analyze(ctx context.Context) (ipc.Analysis_result, error) {
	last, flight, err := a.begin_analysis(ctx, 0)
	if err != nil {
		return ipc.Analysis_result{}, err
	}
	if flight == nil {
		return last, nil
	}

	select {
	case <-flight.done:
		return flight.result, flight.err
	case <-ctx.Done():
		return ipc.Analysis_result{}, ctx.Err()
	}
}

// begin_analysis is the single-flight path every analysis goes through. It
// returns either a recent result to reuse (flight nil), or the flight to wait
// on -- started here, or the one already running.
//
// spacing, when positive, refuses (errors.Is err_analysis_spaced) to start a
// new run when the last one started less than spacing ago, whether that run
// succeeded or failed; a run already in flight is still joined, and a recent
// result is still reused.
func (a *Application) begin_analysis(ctx context.Context, spacing time.Duration) (ipc.Analysis_result, *analysis_flight, error) {
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return ipc.Analysis_result{}, nil, map_error(err)
	}

	// The counter is read before the state: a connect that starts between
	// the two shows in one of them, and one that starts after shows in the
	// counter when the run ends.
	before := a.manager.Transitions()
	if !connection_is_down(a.manager.State()) {
		return ipc.Analysis_result{}, nil, fmt.Errorf("%w: the connection is %s", ipc.Err_tunnel_up, a.manager.State())
	}

	cache := &a.analysis
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := a.now()
	if cache.has {
		if age := now.Sub(cache.last.Measured_at); age >= 0 && age < analysis_reuse {
			return cache.last, nil, nil
		}
	}
	flight := cache.flight
	if flight == nil {
		if spacing > 0 && !cache.last_started.IsZero() && now.Sub(cache.last_started) < spacing {
			return ipc.Analysis_result{}, nil, err_analysis_spaced
		}
		flight = &analysis_flight{done: make(chan struct{})}
		cache.flight = flight
		cache.last_started = now
		// Detached from this caller: the others waiting on the same run
		// must not lose it because the first to ask went away.
		go a.run_analysis(context.WithoutCancel(ctx), token, before, flight)
	}
	return ipc.Analysis_result{}, flight, nil
}

// Network_changed reacts to the machine's network changing. With a tunnel up
// or coming up it repins the transport route and runs one monitoring pass
// (connection.Manager.Network_changed). With the tunnel down it re-measures
// what the network blocks, in the background, at most once per
// analysis_network_spacing, and only for a signed-in device; anything else
// is logged and dropped. It returns without waiting for the measurement.
func (a *Application) Network_changed(ctx context.Context) {
	if ctx.Err() != nil {
		return // the service is stopping
	}
	if !connection_is_down(a.manager.State()) {
		a.manager.Network_changed(ctx, a.now())
		return
	}
	if since := a.now().Sub(a.manager.Changed_at()); since >= 0 && since < network_change_after_disconnect {
		a.logger.Info("network changed right after the connection went down; not analysing")
		return
	}

	_, flight, err := a.begin_analysis(ctx, analysis_network_spacing)
	if err != nil {
		a.logger.Info("network changed; analysis not started", "reason", err.Error())
		return
	}
	if flight == nil {
		a.logger.Info("network changed; the last analysis is recent enough")
		return
	}
	a.logger.Info("network changed; analysing in the background")
	go func() {
		select {
		case <-flight.done:
		case <-ctx.Done():
			return
		}
		if flight.err != nil {
			a.logger.Info("network changed; analysis failed", "error", flight.err)
			return
		}
		// Watchers already re-read on any event; the window asks for the
		// last analysis when one arrives.
		a.bus.Publish(events.Event{Kind: events.Kind_diagnostic_completed, At: a.now()})
	}()
}

// Analysis answers Command_analysis: the last result, without probing.
func (a *Application) Analysis(ctx context.Context) (ipc.Last_analysis, error) {
	a.analysis.mu.Lock()
	defer a.analysis.mu.Unlock()
	if !a.analysis.has {
		return ipc.Last_analysis{}, nil
	}
	last := a.analysis.last
	return ipc.Last_analysis{Present: true, Result: &last}, nil
}

// run_analysis does one run and publishes its outcome to the flight. Only a
// successful result is kept as the last one.
func (a *Application) run_analysis(ctx context.Context, token string, before uint64, flight *analysis_flight) {
	result, err := a.measure(ctx, token, before)

	cache := &a.analysis
	cache.mu.Lock()
	flight.result, flight.err = result, err
	if err == nil {
		cache.last, cache.has = result, true
	}
	cache.flight = nil
	cache.mu.Unlock()
	close(flight.done)
}

func (a *Application) measure(ctx context.Context, token string, before uint64) (ipc.Analysis_result, error) {
	ctx, cancel := context.WithTimeout(ctx, analysis_run_timeout)
	defer cancel()

	catalog, err := a.control_plane.Reach_targets(ctx, token)
	if err != nil {
		return ipc.Analysis_result{}, map_error(err)
	}
	targets := make([]reach.Target, 0, len(catalog.Targets))
	for _, t := range catalog.Targets {
		targets = append(targets, reach.Target{Key: t.Key, Host: t.Host, Port: t.Port, Country: t.Country, Label: t.Label})
	}
	results, err := a.reach_prober.Probe(ctx, targets)
	if err != nil {
		return ipc.Analysis_result{}, fmt.Errorf("core: analysis: probing: %w", err)
	}

	if a.manager.Transitions() != before || !connection_is_down(a.manager.State()) {
		a.logger.Info("analysis discarded: the connection changed while it ran")
		return ipc.Analysis_result{}, fmt.Errorf("%w: the connection changed while the network was being measured", ipc.Err_tunnel_up)
	}

	judged := reach.Judge(results)
	result := ipc.Analysis_result{
		Measured_at: a.now(),
		Country:     catalog.Country,
		Control:     string(judged.Control),
		Checks:      make([]ipc.Reach_check, 0, len(judged.Checks)),
	}
	for _, c := range judged.Checks {
		result.Checks = append(result.Checks, ipc.Reach_check{
			Key: c.Target.Key, Label: c.Target.Label, Host: c.Target.Host, Control: c.Control,
			Dns: string(c.Dns), Tcp: string(c.Tcp), Tls: string(c.Tls),
			Failure: string(c.Failure), Verdict: string(c.Verdict), Method: string(c.Method),
		})
	}
	return result, nil
}

// connection_is_down is true when no tunnel is up and none is on its way:
// disconnected, or failed after every attempt.
func connection_is_down(state connection.State) bool {
	return state == connection.State_disconnected || state == connection.State_failed
}
