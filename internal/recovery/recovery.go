package recovery

import (
	"errors"
	"time"
)

// Target is one node-and-protocol pair a connection could use.
type Target struct {
	Node_id  string
	Protocol string
}

// Reason names why a switch is being considered.
type Reason int

const (
	Reason_hard_fail Reason = iota // the tunnel never came up
	Reason_degraded                // it came up and then got worse
)

// Config is every number this package's policy needs. Time itself is never
// stored here or anywhere in this package -- every method that needs it
// takes now as a parameter, so every cooldown, budget and window is tested
// without a clock, a goroutine or a network.
type Config struct {
	Max_switch_attempts int
	Protocol_cooldown   time.Duration
	Min_stable_time     time.Duration
	Degradation_window  time.Duration
}

// Default_config returns the values this client actually uses. Each one is
// a decision the documents left as a range:
//
//   - Max_switch_attempts: 3, fixed by CLIENT_AUTO_SWITCH.md section 10.
//   - Protocol_cooldown: 5m, the low end of the documented 5-10m -- a long
//     cooldown strands a person on a worse route for no gain.
//   - Min_stable_time: 30s, the low end of the documented 30-60s -- the
//     cooldown already prevents ping-pong, so a full minute on a connection
//     already judged bad is a minute wasted.
//   - Degradation_window: 15s, the middle of the documented 10-20s; part B
//     holds this timer, this package only carries the number.
func Default_config() Config {
	return Config{
		Max_switch_attempts: 3,
		Protocol_cooldown:   5 * time.Minute,
		Min_stable_time:     30 * time.Second,
		Degradation_window:  15 * time.Second,
	}
}

// Err_no_target is what Next_target returns when nothing survives the
// filtering, even after the fail history of this attempt was cleared.
var Err_no_target = errors.New("recovery: nothing left to try")

// Err_budget_spent is what Next_target returns once this attempt has used
// up its switches. Part B turns this into the failed state and tells the
// person.
var Err_budget_spent = errors.New("recovery: the switch budget for this attempt is spent")

// Err_too_soon is what Next_target returns when a degraded connection has
// not been stable long enough to be worth leaving.
var Err_too_soon = errors.New("recovery: the current connection has not been stable long enough to leave it")
