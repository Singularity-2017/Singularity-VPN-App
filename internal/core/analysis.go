package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// network_change_after_disconnect is how long after the connection went
// down a network change is taken for the tunnel's own adapter going away
// rather than the network under the machine changing. The adapter, once
// deleted, can no longer be recognised by name in the notification.
const network_change_after_disconnect = 15 * time.Second

// The times that pace the automatic runs, variables only so a test can make
// them short. analysis_check_every is the period of the check that keeps a
// machine sitting on one network measured daily; analysis_start_settle is how
// long after the service starts the first check waits, so a connection left
// up has come back by then; analysis_down_settle is how long after the
// connection goes down its check waits, for the tunnel's adapter to be gone.
var (
	analysis_check_every  = 30 * time.Minute
	analysis_start_settle = 20 * time.Second
	analysis_down_settle  = network_change_after_disconnect
)

// analysis_automatic_spacing is the least time between two automatic runs,
// successful or not, so a flapping link does not turn into a probe storm.
const analysis_automatic_spacing = 5 * time.Minute

// What started an automatic run. Only these words reach the log.
const (
	analysis_trigger_network_changed = "network_changed"
	analysis_trigger_connection_down = "connection_down"
	analysis_trigger_service_start   = "service_start"
	analysis_trigger_periodic        = "periodic"
)

// err_analysis_network_changed says a run was thrown away because the network
// under the machine changed while it measured.
var err_analysis_network_changed = errors.New("analysis: network changed while measuring")

// analysis_run_timeout bounds a whole run -- the catalog fetch and the
// probes -- on top of the bounds each of them has.
const analysis_run_timeout = 45 * time.Second

// analysis_flight is one run in progress. Every caller that arrives while it
// runs waits on done and gets the same answer.
type analysis_flight struct {
	generation uint64

	// network is the key of the network the run began on, empty when it could
	// not be told. A result is kept against it, and thrown away if the
	// network under the machine is another one by the end.
	network string

	done   chan struct{}
	result ipc.Analysis_result
	err    error
}

// analysis_cache keeps the last successful result and any run in progress.
// A restored result is explicitly historical until measured again.
type analysis_cache struct {
	generation uint64
	loaded     bool
	stale      bool
	mu         sync.Mutex
	last       ipc.Analysis_result
	has        bool
	flight     *analysis_flight

	// networks is, per network key, when it was last measured successfully.
	// It is what makes an automatic run due or not; see analysis_due. It is
	// guarded by mu like everything above.
	networks        map[string]time.Time
	networks_loaded bool

	// last_automatic is when the last automatic run started; see
	// analysis_automatic_spacing.
	last_automatic time.Time
}

// Analyze answers Command_analyze: it probes the catalog directly and says
// what the network under the device blocks.
//
// The tunnel has to be down, and has to stay down for the whole run: probes
// that went through our own node would measure the node and call it the ISP.
// So the manager's transition counter is read before anything else and again
// when the probes are done, and a result is discarded -- as tunnel_up -- if
// the counter moved, however briefly the state changed.
//
// A manual run does not wait for the day an automatic one does: the person
// asked.
func (a *Application) Analyze(ctx context.Context) (ipc.Analysis_result, error) {
	last, flight, err := a.begin_analysis(ctx, false)
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
// automatic marks a run nobody asked for: it also needs the network to be
// known and not measured within the day (analysis_due), and is refused with
// err_analysis_unknown_network or err_analysis_not_due otherwise. A manual run
// is never refused for that. Either way a run already in flight is joined,
// and a recent result is reused.
func (a *Application) begin_analysis(ctx context.Context, automatic bool) (ipc.Analysis_result, *analysis_flight, error) {
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

	// Read before the lock: it can take a moment, and it is the network this
	// run is about, not the one at the end of it.
	network := a.current_network(ctx)

	cache := &a.analysis
	cache.mu.Lock()
	defer cache.mu.Unlock()
	a.restore_analysis_locked()
	a.restore_networks_locked()
	now := a.now()
	if automatic {
		if err := analysis_due(cache.networks, network, now); err != nil {
			return ipc.Analysis_result{}, nil, err
		}
		if cache.flight == nil && !cache.last_automatic.IsZero() && now.Sub(cache.last_automatic) < analysis_automatic_spacing {
			return ipc.Analysis_result{}, nil, err_analysis_spaced
		}
	}
	if cache.has && !cache.stale {
		if age := now.Sub(cache.last.Measured_at); age >= 0 && age < analysis_reuse {
			return cache.last, nil, nil
		}
	}
	flight := cache.flight
	if flight == nil {
		flight = &analysis_flight{done: make(chan struct{}), generation: cache.generation, network: network}
		cache.flight = flight
		if automatic {
			cache.last_automatic = now
		}
		// Detached from this caller: the others waiting on the same run
		// must not lose it because the first to ask went away.
		go a.run_analysis(context.WithoutCancel(ctx), token, before, flight)
	}
	return ipc.Analysis_result{}, flight, nil
}

// analyze_if_due is the one door the automatic runs go through, whatever
// started them: begin_analysis with the day's rule, in the background. It
// returns without waiting for the measurement, and tells the bus when one
// succeeds. trigger is one of the analysis_trigger_ words; only it and a
// category of why nothing ran are logged.
func (a *Application) analyze_if_due(ctx context.Context, trigger string) {
	// The periodic check finds nothing to do almost every time; saying so
	// every half hour would only bury the log.
	skipped := slog.LevelInfo
	if trigger == analysis_trigger_periodic {
		skipped = slog.LevelDebug
	}

	_, flight, err := a.begin_analysis(ctx, true)
	if err != nil {
		a.logger.Log(ctx, skipped, "analysis not started", "trigger", trigger, "why", analysis_error_category(err))
		return
	}
	if flight == nil {
		a.logger.Log(ctx, skipped, "analysis not started", "trigger", trigger, "why", "recent_result")
		return
	}
	a.logger.Info("analysing in the background", "trigger", trigger)
	go func() {
		select {
		case <-flight.done:
		case <-ctx.Done():
			return
		}
		if flight.err != nil {
			a.logger.Info("analysis failed", "trigger", trigger, "why", analysis_error_category(flight.err))
			return
		}
		// Watchers already re-read on any event; the window asks for the
		// last analysis when one arrives.
		a.bus.Publish(events.Event{Kind: events.Kind_diagnostic_completed, At: a.now()})
	}()
}

// analysis_error_category names why an analysis did not run or did not
// finish, in a word that is safe to write in a log: never the error's own
// text, which may carry what the control plane said.
func analysis_error_category(err error) string {
	switch {
	case errors.Is(err, ipc.Err_not_signed_in):
		return "not_signed_in"
	case errors.Is(err, ipc.Err_unauthorized):
		return "unauthorized"
	case errors.Is(err, ipc.Err_tunnel_up):
		return "connection_up"
	case errors.Is(err, err_analysis_unknown_network):
		return "unknown_network"
	case errors.Is(err, err_analysis_not_due):
		return "not_due"
	case errors.Is(err, err_analysis_spaced):
		return "spaced"
	case errors.Is(err, err_analysis_network_changed):
		return "network_changed"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "error"
	}
}

// Network_changed reacts to the machine's network changing. With a tunnel up
// or coming up it repins the transport route and runs one monitoring pass
// (connection.Manager.Network_changed). With the tunnel down it asks for an
// automatic measurement of what the network blocks -- which happens only if
// the network is a new one, or one not measured within the day; see
// analysis_due -- and only for a signed-in device; anything else is logged and
// dropped. It returns without waiting for the measurement.
func (a *Application) Network_changed(ctx context.Context) {
	a.manager.Evidence_network_changed(ctx)
	if ctx.Err() != nil {
		return // the service is stopping
	}
	a.analysis.mu.Lock()
	a.restore_analysis_locked()
	a.analysis.stale = true
	a.analysis.generation++
	a.analysis.mu.Unlock()
	a.bus.Publish(events.Event{Kind: events.Kind_network_changed, At: a.now()})
	if !connection_is_down(a.manager.State()) {
		a.manager.Network_changed(ctx, a.now())
		return
	}
	if since := a.now().Sub(a.manager.Changed_at()); since >= 0 && since < network_change_after_disconnect {
		a.logger.Info("network changed right after the connection went down; not analysing")
		return
	}

	a.analyze_if_due(ctx, analysis_trigger_network_changed)
}

// watch_analysis starts the automatic runs that no network change asks for,
// until ctx ends: one shortly after the service starts, one a settle time
// after the connection goes down, and one every analysis_check_every -- the
// last so a machine that stays disconnected on one network is still measured
// daily. Each only asks analyze_if_due, which decides. A state event can be
// dropped when the bus is full; the periodic check covers for that.
func (a *Application) watch_analysis(ctx context.Context) {
	channel, unsubscribe := a.bus.Subscribe()
	defer unsubscribe()
	ticker := time.NewTicker(analysis_check_every)
	defer ticker.Stop()
	start := time.NewTimer(analysis_start_settle)
	defer start.Stop()

	var down_timer *time.Timer
	var down <-chan time.Time
	stop_down := func() {
		if down_timer != nil {
			down_timer.Stop()
		}
		down_timer, down = nil, nil
	}
	defer stop_down()

	for {
		select {
		case <-ctx.Done():
			return
		case <-start.C:
			a.analyze_if_due(ctx, analysis_trigger_service_start)
		case <-ticker.C:
			a.analyze_if_due(ctx, analysis_trigger_periodic)
		case <-down:
			stop_down()
			a.analyze_if_due(ctx, analysis_trigger_connection_down)
		case event, ok := <-channel:
			if !ok {
				return
			}
			if event.Kind != events.Kind_state_changed {
				continue
			}
			change, ok := event.Data.(connection.State_change)
			if !ok {
				continue
			}
			stop_down()
			if connection_is_down(change.To) {
				down_timer = time.NewTimer(analysis_down_settle)
				down = down_timer.C
			}
		}
	}
}

// Analysis answers Command_analysis: the last result, without probing.
func (a *Application) Analysis(ctx context.Context) (ipc.Last_analysis, error) {
	a.analysis.mu.Lock()
	defer a.analysis.mu.Unlock()
	a.restore_analysis_locked()
	if !a.analysis.has {
		return ipc.Last_analysis{}, nil
	}
	last := a.analysis.last
	return ipc.Last_analysis{Present: true, Result: &last, Stale: a.analysis.stale}, nil
}

// run_analysis does one run and publishes its outcome to the flight. Only a
// successful result is kept as the last one, and only a successful result
// marks the network it began on as measured.
func (a *Application) run_analysis(ctx context.Context, token string, before uint64, flight *analysis_flight) {
	result, err := a.measure(ctx, token, before)
	if err == nil && flight.network != "" && a.current_network(ctx) != flight.network {
		a.logger.Info("analysis discarded: the network changed while it ran")
		err = err_analysis_network_changed
	}

	cache := &a.analysis
	cache.mu.Lock()
	// A network change during the run discards it -- unless the network is
	// known by its key, which was compared above: then the change was churn
	// on the same network, and the result stands.
	if err == nil && cache.generation != flight.generation && flight.network == "" {
		err = err_analysis_network_changed
	}
	flight.result, flight.err = result, err
	if err == nil {
		cache.last, cache.has, cache.loaded, cache.stale = result, true, true, false
		if err := a.save_analysis_locked(result); err != nil {
			a.logger.Warn("analysis cache could not be saved")
		}
		// A network whose controls did not answer -- dead, or a captive
		// portal -- is measured again on the next chance, not a day later.
		if result.Control != string(reach.Control_failed) {
			a.record_network_locked(flight.network, result.Measured_at)
		}
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
	// What is probed is the catalog as the person edited it.
	effective, _ := compose_reach_list(catalog.Targets, a.reach_edits_snapshot())
	targets := make([]reach.Target, 0, len(effective))
	for _, t := range effective {
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
