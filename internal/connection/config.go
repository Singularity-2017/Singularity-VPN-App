package connection

import (
	"time"

	"singularity-vpn-app/internal/recovery"
)

// Config is every number the manager needs beyond what recovery.Policy
// already carries. Time itself is never read from inside this package's
// logic -- Tick takes now as a parameter -- so every one of these durations
// is exercised in a test without a clock, a goroutine or a sleep.
type Config struct {
	// Connect_timeout bounds a whole connect, verification included: 30s,
	// so a person waiting to know whether the app works gets an answer
	// instead of an indefinite spinner.
	Connect_timeout time.Duration
	// Protocol_timeout bounds Protocol.Connect alone: 5s, CLIENT_AUTO_SWITCH.md
	// section 5.1 -- past this the protocol itself is a hard failure,
	// independent of how much of Connect_timeout is left.
	Protocol_timeout time.Duration
	// Monitor_interval is how often Run calls Tick while a connection
	// exists: 5s, the low end of the documented 5-10s, because a degraded
	// connection is worth noticing sooner rather than later.
	Monitor_interval time.Duration
	// Reevaluation_interval is how often a healthy connection is offered a
	// better option: 5m, the high end of the documented 2-5m, because an
	// offer nobody asked for should be rare.
	Reevaluation_interval time.Duration
	// Profile_duration_days is what Create_profile asks for: 30, one
	// billing month.
	Profile_duration_days int
	// Recovery is handed to recovery.Policy unchanged; this package owns
	// no recovery numbers of its own.
	Recovery recovery.Config
}

// with_defaults fills in every value left unset. A zero here does not mean
// "no limit": Run builds a ticker straight from Monitor_interval, and
// time.NewTicker panics on a non-positive one -- inside a service that owns
// a person's network route, where a panic is the worst outcome available. A
// Config nobody filled in must not be able to reach that, so an unset value
// becomes the documented default rather than a hole.
func (c Config) with_defaults() Config {
	defaults := Default_config()
	if c.Connect_timeout <= 0 {
		c.Connect_timeout = defaults.Connect_timeout
	}
	if c.Protocol_timeout <= 0 {
		c.Protocol_timeout = defaults.Protocol_timeout
	}
	if c.Monitor_interval <= 0 {
		c.Monitor_interval = defaults.Monitor_interval
	}
	if c.Reevaluation_interval <= 0 {
		c.Reevaluation_interval = defaults.Reevaluation_interval
	}
	if c.Profile_duration_days <= 0 {
		c.Profile_duration_days = defaults.Profile_duration_days
	}
	if c.Recovery.Max_switch_attempts <= 0 {
		c.Recovery.Max_switch_attempts = defaults.Recovery.Max_switch_attempts
	}
	if c.Recovery.Protocol_cooldown <= 0 {
		c.Recovery.Protocol_cooldown = defaults.Recovery.Protocol_cooldown
	}
	if c.Recovery.Min_stable_time <= 0 {
		c.Recovery.Min_stable_time = defaults.Recovery.Min_stable_time
	}
	if c.Recovery.Degradation_window <= 0 {
		c.Recovery.Degradation_window = defaults.Recovery.Degradation_window
	}
	return c
}

// Default_config returns the values this client actually uses.
func Default_config() Config {
	return Config{
		Connect_timeout:       30 * time.Second,
		Protocol_timeout:      5 * time.Second,
		Monitor_interval:      5 * time.Second,
		Reevaluation_interval: 5 * time.Minute,
		Profile_duration_days: 30,
		Recovery:              recovery.Default_config(),
	}
}
