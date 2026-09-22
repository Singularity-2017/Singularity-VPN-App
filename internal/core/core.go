package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/protocols"
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
	Recommend(ctx context.Context, token string) (api.Recommendation, error)
	Create_profile(ctx context.Context, token, protocol, node_id string, duration_days int) (api.Profile, error)
	Fetch_config(ctx context.Context, token, client_id, protocol, node_id string) (api.Vpn_config, error)
}

// Dependencies is everything the application needs and nothing it invents,
// except Logger and Now: those two may default, the way connection.Manager
// lets its own clock default, because a discarding logger and a real clock
// are the only pieces of this package that are reasonable to assume in
// production and safe to leave unfaked in a test that does not care about
// them.
type Dependencies struct {
	Control_plane Control_plane
	Secrets       security.Secure_store
	Store         connection.State_store
	Protocols     protocols.Manager
	Prober        diagnostics.Prober
	Bus           *events.Bus
	Config        connection.Config
	Device_name   string       // what a minted device token is called on the account
	Logger        *slog.Logger // nil means slog.New(slog.DiscardHandler)
	Now           func() time.Time
}

// Application is the running program: the connection state machine, wired
// to a real control plane, a real secure store and a real set of protocol
// adapters, speaking ipc.Service to whatever serves the pipe.
type Application struct {
	manager       *connection.Manager
	control_plane Control_plane
	secrets       security.Secure_store
	store         connection.State_store
	bus           *events.Bus
	device_name   string
	logger        *slog.Logger
	now           func() time.Time
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

	manager, err := connection.New_manager(connection.Dependencies{
		Control_plane: dependencies.Control_plane,
		Token:         &token_source{secrets: dependencies.Secrets},
		Protocols:     dependencies.Protocols,
		Prober:        dependencies.Prober,
		Policy:        recovery.New_policy(recovery_config_with_defaults(dependencies.Config.Recovery)),
		Store:         dependencies.Store,
		Bus:           dependencies.Bus,
		Now:           now,
		Config:        dependencies.Config,
	})
	if err != nil {
		return nil, fmt.Errorf("core: building the connection manager: %w", err)
	}

	return &Application{
		manager:       manager,
		control_plane: dependencies.Control_plane,
		secrets:       dependencies.Secrets,
		store:         dependencies.Store,
		bus:           dependencies.Bus,
		device_name:   dependencies.Device_name,
		logger:        logger,
		now:           now,
	}, nil
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
// Start does not auto-connect. state.State.Connect_on_start exists and is
// the right switch, but nothing can set it yet -- there is no settings
// command in the protocol and no installer. A service that connected on its
// own because of a field nobody can turn off would be worse than one that
// waits for a person to ask. Stage 5 turns this on.
func (a *Application) Start(ctx context.Context) error {
	go func() {
		_ = a.manager.Run(ctx)
	}()

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
				a.logger.Info("event", "kind", string(event.Kind), "at", event.At, "session_id", event.Session_id)
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
