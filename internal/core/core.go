package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"singularity-vpn-app/internal/statistics"
	"sync"
	"sync/atomic"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/evidence"
	"singularity-vpn-app/internal/journal"
	"singularity-vpn-app/internal/matchserver"
	"singularity-vpn-app/internal/platform"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/reach"
	"singularity-vpn-app/internal/recovery"
	"singularity-vpn-app/internal/security"
)

// stop_timeout bounds how long Stop waits for the protocol adapter to tear
// a tunnel down. It uses connection.Config.Connect_timeout's value rather
// than inventing its own number: tearing a tunnel down is not slower than
// bringing one up, and a service under a service control manager's own
// shutdown timer needs this to be a bounded wait, not an indefinite one.
const stop_timeout = 30 * time.Second

// Control_plane is everything the application asks of the control plane:
// the three calls connection.Manager makes, plus the two sign-in makes.
// *api.Client satisfies it; a test does not need a server.
type Control_plane interface {
	Login(ctx context.Context, username, password string) (string, error)
	Mint_device_token(ctx context.Context, session_token, name string) (api.Device, error)
	Recommend(ctx context.Context, token string, request api.Recommend_request) (api.Recommendation, error)
	Create_profile(ctx context.Context, token, protocol, node_id string, duration_days int) (api.Profile, error)
	Fetch_config(ctx context.Context, token, client_id, protocol, node_id, mode string) (api.Vpn_config, error)
	Servers(ctx context.Context, token string) ([]api.Server, error)
	Reach_targets(ctx context.Context, token string) (api.Reach_catalog, error)
}

// Reach_prober probes the catalog of targets directly and reports how far
// each got. *reach.Prober satisfies it; a test hands in a fake and touches
// no network.
type Reach_prober interface {
	Probe(ctx context.Context, targets []reach.Target) ([]reach.Result, error)
}

// Dependencies is everything the application needs and nothing it invents,
// except Logger and Now: those two may default, the way connection.Manager
// lets its own clock default, because a discarding logger and a real clock
// are the only pieces of this package that are reasonable to assume in
// production and safe to leave unfaked in a test that does not care about
// them.
type Dependencies struct {
	Statistics_path     string
	Analysis_cache_path string
	// Analysis_networks_path is where the time of each network's last
	// successful measurement is kept, by network key; empty keeps none.
	Analysis_networks_path string
	// Reach_list_path is where the person's edits to the service list are
	// kept; empty keeps them in memory only.
	Reach_list_path string
	// Network_key names the network under the machine by a salted hash, for
	// the once-a-day automatic measurement. nil, an error or an empty key
	// mean the network is not known, and nothing is measured automatically;
	// a manual measurement does not need it.
	Network_key   func(ctx context.Context) (string, error)
	Evidence      *evidence.Collector
	Control_plane Control_plane
	Secrets       security.Secure_store
	Store         connection.State_store
	Protocols     protocols.Manager
	Prober        diagnostics.Prober
	Reach_prober  Reach_prober
	Pinger        platform.Pinger // the latency to every listed server; see Application.Ping_servers
	Game_pinger   Game_pinger     // a running game's latency to its match server; nil measures none
	Bus           *events.Bus
	Config        connection.Config
	Device_name   string       // what a minted device token is called on the account
	Logger        *slog.Logger // nil means slog.New(slog.DiscardHandler)
	// Journal is what Command_journal reads; the logger above feeds it.
	// nil means an empty journal.
	Journal *journal.Journal
	// Matches is where the servers detected for matches, and the
	// player's confirmations of them, are kept; nil keeps them in memory.
	Matches *matchserver.Store
	Now     func() time.Time

	// Log_path is the service log whose newest part a problem report
	// attaches; the one rolled over before it is read from path+".1".
	// Empty attaches no log.
	Log_path string
	// Network_summary describes the machine's network adapters for a
	// problem report -- classes of address, never the addresses. nil, or
	// an error, leaves the network out of the report.
	Network_summary func(context.Context) (any, error)
	// Os_version is the operating system's version, for a problem report;
	// empty leaves it out rather than guessing.
	Os_version string
}

// Application is the running program: the connection state machine, wired
// to a real control plane, a real secure store and a real set of protocol
// adapters, speaking ipc.Service to whatever serves the pipe.
type Application struct {
	statistics_stop      chan struct{}
	statistics_stop_once sync.Once
	statistics_save_mu   sync.Mutex
	statistics           *statistics.Store
	statistics_error     atomic.Bool
	analysis_cache_path  string
	manager              *connection.Manager
	control_plane        Control_plane
	secrets              security.Secure_store
	store                connection.State_store
	bus                  *events.Bus
	device_name          string
	logger               *slog.Logger
	journal              *journal.Journal
	now                  func() time.Time

	reach_prober Reach_prober
	analysis     analysis_cache

	// analysis_networks_path and network_key give the automatic runs their
	// once-a-day rule; see analysis_due.
	analysis_networks_path string
	network_key            func(ctx context.Context) (string, error)

	// reach_list_path and reach_list are the person's edits to the catalog
	// the analysis probes; see compose_reach_list.
	reach_list_path string
	reach_list      reach_list_state

	// pinger and resolve_ip4 are what Ping_servers measures with.
	// resolve_ip4 is a field only so a test can answer a name without a
	// network.
	pinger      platform.Pinger
	resolve_ip4 func(ctx context.Context, host string) ([]netip.Addr, error)

	// hosts is node_id -> host for the servers the last Servers call listed
	// with an address. The addresses stay here: the window is told only how
	// long each node took to answer. hosts_mu guards it, because Servers
	// replaces it on one request while Ping_servers reads it on another.
	hosts_mu sync.Mutex
	hosts    map[string]string

	game_pinger Game_pinger
	game_pings  game_pings
	node_rtts   node_game_rtts
	matches     *matchserver.Detector

	// evidence names the network under the machine for a problem report;
	// nil is a collector that knows nothing.
	evidence        *evidence.Collector
	log_path        string
	network_summary func(context.Context) (any, error)
	os_version      string
	// drafts holds the problem reports the person has been shown and not
	// yet sent; see problem_drafts.
	drafts problem_drafts
}

// New_application builds an Application, refusing a Dependencies missing
// anything it cannot invent, each named in its own error, the way
// connection.New_manager does. Device_name empty is refused too: a device
// token nobody can identify on the account page is a credential the person
// cannot revoke, and revoking it is the whole reason the endpoint exists
// (design section 5).
func New_application(dependencies Dependencies) (*Application, error) {
	switch {
	case dependencies.Control_plane == nil:
		return nil, fmt.Errorf("core: Dependencies.Control_plane is required")
	case dependencies.Secrets == nil:
		return nil, fmt.Errorf("core: Dependencies.Secrets is required")
	case dependencies.Store == nil:
		return nil, fmt.Errorf("core: Dependencies.Store is required")
	case dependencies.Protocols == nil:
		return nil, fmt.Errorf("core: Dependencies.Protocols is required")
	case dependencies.Prober == nil:
		return nil, fmt.Errorf("core: Dependencies.Prober is required")
	case dependencies.Reach_prober == nil:
		return nil, fmt.Errorf("core: Dependencies.Reach_prober is required")
	case dependencies.Pinger == nil:
		return nil, fmt.Errorf("core: Dependencies.Pinger is required")
	case dependencies.Bus == nil:
		return nil, fmt.Errorf("core: Dependencies.Bus is required")
	case dependencies.Device_name == "":
		return nil, fmt.Errorf("core: Dependencies.Device_name is required")
	}

	logger := dependencies.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}

	stats, stats_err := open_statistics(dependencies.Statistics_path, now)
	if stats_err != nil {
		logger.Warn("local statistics unavailable")
	}
	manager, err := connection.New_manager(connection.Dependencies{
		Event_observer:  statistics_observer(stats),
		Sample_observer: statistics_sample_observer(stats),
		Control_plane:   dependencies.Control_plane,
		Evidence:        dependencies.Evidence,
		Token:           &token_source{secrets: dependencies.Secrets},
		Protocols:       dependencies.Protocols,
		Prober:          dependencies.Prober,
		Policy:          recovery.New_policy(recovery_config_with_defaults(dependencies.Config.Recovery)),
		Store:           dependencies.Store,
		Bus:             dependencies.Bus,
		Now:             now,
		Config:          dependencies.Config,
		Logger:          logger,
	})
	if err != nil {
		return nil, fmt.Errorf("core: building the connection manager: %w", err)
	}

	matches := dependencies.Matches
	if matches == nil {
		matches, _ = matchserver.Open_store("")
	}

	app := &Application{
		statistics_stop:     make(chan struct{}),
		statistics:          stats,
		analysis_cache_path: dependencies.Analysis_cache_path,
		manager:             manager,
		control_plane:       dependencies.Control_plane,
		secrets:             dependencies.Secrets,
		store:               dependencies.Store,
		bus:                 dependencies.Bus,
		device_name:         dependencies.Device_name,
		logger:              logger,
		journal:             dependencies.Journal,
		now:                 now,
		reach_prober:        dependencies.Reach_prober,

		analysis_networks_path: dependencies.Analysis_networks_path,
		network_key:            dependencies.Network_key,
		reach_list_path:        dependencies.Reach_list_path,

		pinger:      dependencies.Pinger,
		game_pinger: dependencies.Game_pinger,
		matches:     matchserver.New_detector(matches),

		evidence:        dependencies.Evidence,
		log_path:        dependencies.Log_path,
		network_summary: dependencies.Network_summary,
		os_version:      dependencies.Os_version,

		resolve_ip4: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		},
	}
	app.statistics_error.Store(stats_err != nil)
	return app, nil
}

// recovery_config_with_defaults fills in every value the caller left unset,
// the same way connection.Config.with_defaults does for the numbers
// connection.Manager keeps for itself. That method is unexported, and the
// recovery.Policy this package builds is a second, independent consumer of
// the same Config.Recovery -- so a Dependencies.Config left at its zero
// value must not hand recovery.New_policy a Max_switch_attempts of zero,
// which would refuse every switch outright instead of behaving like the
// documented default.
func recovery_config_with_defaults(config recovery.Config) recovery.Config {
	defaults := recovery.Default_config()
	if config.Max_switch_attempts <= 0 {
		config.Max_switch_attempts = defaults.Max_switch_attempts
	}
	if config.Protocol_cooldown <= 0 {
		config.Protocol_cooldown = defaults.Protocol_cooldown
	}
	if config.Min_stable_time <= 0 {
		config.Min_stable_time = defaults.Min_stable_time
	}
	if config.Degradation_window <= 0 {
		config.Degradation_window = defaults.Degradation_window
	}
	return config
}

// Start launches manager.Run in a goroutine and returns immediately -- it
// must not block, because the caller's next job is to start serving the
// pipe. It also launches an event-logging goroutine: subscribe to the bus,
// and log every event at info with its kind, time and session id. That is
// what gives the service a log worth reading, and it costs one subscriber.
//
// Start runs the monitor and, when the person left the machine connected,
// connects again: Connect_on_start is set by their connect and cleared only
// by their disconnect or sign-out, never by the service stopping, so a
// reboot puts them back where they were (design section 10). A person who
// pressed disconnect stays disconnected.
func (a *Application) Start(ctx context.Context) error {
	if a.statistics != nil {
		go a.checkpoint_statistics(ctx)
	}
	go a.manager.Run_evidence(ctx)
	go func() {
		_ = a.manager.Run(ctx)
	}()
	go a.resume(ctx)
	if a.game_pinger != nil {
		go a.measure_games(ctx)
	}
	if a.network_key != nil {
		go a.watch_analysis(ctx)
	}

	channel, unsubscribe := a.bus.Subscribe()
	go func() {
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-channel:
				if !ok {
					return
				}
				attrs := []any{"kind", string(event.Kind), "at", event.At, "session_id", event.Session_id}
				if detail, ok := event.Data.(diagnostics.Degradation); ok && (event.Kind == events.Kind_connection_degraded || event.Kind == events.Kind_connection_blip) {
					attrs = append(attrs, "degraded_latency", detail.High_latency, "degraded_probes", detail.Failed_probes, "loss_percent", detail.Packet_loss*100)
					if detail.Resent_probes {
						attrs = append(attrs, "degraded_resent", true, "resent_percent", detail.Resent*100)
					}
					if detail.Udp_lost {
						attrs = append(attrs, "degraded_udp", true, "udp_loss_percent", detail.Udp_loss*100)
					}
					if detail.Rtt > 0 {
						attrs = append(attrs, "rtt_ms", float64(detail.Rtt)/float64(time.Millisecond))
					}
					if detail.High_latency && detail.Baseline_rtt > 0 {
						attrs = append(attrs, "baseline_rtt_ms", float64(detail.Baseline_rtt)/float64(time.Millisecond))
					}
				}
				// Which node and protocol a tunnel came up on, while the
				// session the event names is still the current one.
				if s, ok := a.manager.Session(); ok && s.Id == event.Session_id {
					attrs = append(attrs, "node_id", s.Node_id, "protocol", s.Protocol, "tunnel", s.Tunnel)
				}
				a.logger.Info("event", attrs...)
			}
		}
	}()

	return nil
}

// Stop disconnects if there is anything up, using its own context so that a
// cancelled caller context cannot leave a tunnel and a routing table behind,
// then closes the bus. It is idempotent: connection.Manager.Disconnect is
// itself a no-op when nothing is connected.
func (a *Application) Stop(ctx context.Context) error {
	// WithoutCancel rather than Background: the caller's context is almost
	// always the one that has just been cancelled to trigger this
	// shutdown, and teardown is the part that must still run -- it is what
	// takes the routes and the DNS back off the machine. Its values are
	// kept, its cancellation is not, and the wait is bounded here instead.
	stop_ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stop_timeout)
	defer cancel()

	err := a.manager.Disconnect(stop_ctx)
	a.bus.Close()
	if a.statistics != nil {
		a.statistics_stop_once.Do(func() { close(a.statistics_stop) })
		a.statistics.Finish()
		a.save_statistics()
	}
	if err != nil {
		return fmt.Errorf("core: stop: disconnecting: %w", err)
	}
	return nil
}

// token_source implements connection.Token_source by reading
// security.Key_device_token from the secure store on every call. Not
// cached: the store is the source of truth, a DPAPI read is cheap next to
// opening a tunnel, and a cached token would survive a sign-out.
//
// A missing key is not translated to ipc.Err_not_signed_in here -- it is
// wrapped with context and left carrying security.Err_not_found, because
// map_error in errors.go is the one place that translation happens for
// every path a Fault can reach it through, not just this one.
type token_source struct {
	secrets security.Secure_store
}

func (t *token_source) Token(ctx context.Context) (string, error) {
	value, err := t.secrets.Get(security.Key_device_token)
	if err != nil {
		if errors.Is(err, security.Err_not_found) {
			return "", fmt.Errorf("core: no device token is stored: %w", err)
		}
		return "", fmt.Errorf("core: reading the device token: %w", err)
	}
	return string(value), nil
}

// resume_delays spaces the connect attempts made at start. The service
// starts early in boot, often before the network has an address, so the
// first attempt failing says little; the later ones cover a slow Wi-Fi
// association without retrying forever on a machine that is simply
// offline. Recovery takes over once a tunnel is up.
var resume_delays = []time.Duration{0, 5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}

// resume connects at start if the person left the machine connected. The
// decision -- still wanted, nothing else moved the state machine -- is taken
// inside the manager under the same lock as the connect, so a disconnect
// pressed meanwhile wins. It reconnects in auto mode: a node chosen by hand
// through svpn-ctl is not remembered across a restart.
func (a *Application) resume(ctx context.Context) {
	for _, delay := range resume_delays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if signed_in, err := a.signed_in(); err != nil || !signed_in {
			return
		}
		attempted, err := a.manager.Connect_if_wanted(ctx, connection.Connect_request{Mode: connection.Mode_auto})
		if !attempted || err == nil || ctx.Err() != nil {
			if err != nil && !attempted {
				a.logger.Warn("could not read whether a connection is wanted", "err", err)
			}
			if attempted && err == nil {
				a.logger.Info("reconnected at start: the machine was left connected")
			}
			return
		}
		a.logger.Warn("reconnect at start failed; will try again", "err", err)
	}
}
