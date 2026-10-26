package recovery

import "time"

// Policy is the pure decision behind one connection's recovery: it answers
// "may I switch, and to what", and never performs the switch itself. Part B
// owns the goroutine and the actual reconnect; this type owns only the
// budget, the cooldowns and the anti-flapping window.
//
// Deviation from CLIENT_AUTO_SWITCH.md section 11.2, deliberate: that
// document writes cooldowns[protocol]. Here a cooldown is keyed by the
// whole Target, node and protocol together. WireGuard failing on DE-01 says
// nothing about WireGuard on NL-02, and blocking the second for five
// minutes would strand a person over a fault that was never the protocol's.
type Policy struct {
	config Config

	attempts int
	// failed holds every target that has failed during the current
	// attempt. It is per attempt, not per session: Record_success and the
	// "nothing left" fallback in Next_target both clear it, but neither
	// touches cooldowns.
	failed map[Target]bool
	// cooldown_until holds, for every target that has ever failed, the
	// moment its cooldown expires. It survives across attempts on purpose:
	// a protocol that failed five minutes ago is not vindicated by a
	// different one working. The single exception is the target a
	// Record_success names -- see there.
	cooldown_until map[Target]time.Time

	stable_since     time.Time
	stable_since_set bool // false until the first Record_success ever
}

// New_policy builds a Policy that starts with no history: no failures, no
// cooldowns and no recorded stability.
func New_policy(config Config) *Policy {
	return &Policy{
		config:         config,
		failed:         make(map[Target]bool),
		cooldown_until: make(map[Target]time.Time),
	}
}

// Next_target answers "may I switch, and to what", applying the rules in
// order:
//
//  1. Budget: an attempt that has already used Max_switch_attempts switches
//     is refused outright.
//  2. Anti-flapping, and only where it belongs: a degraded reason is
//     refused until Min_stable_time has passed since the last recorded
//     success. A hard failure never waited on anything, so there is nothing
//     to have been stable, and the window does not apply to it. A policy
//     that has never recorded a success is never too soon.
//  3. Filtering: drop current, drop every target already failed during this
//     attempt, drop every target whose cooldown has not expired at now.
//  4. Choice: the first survivor, in the order the caller passed it -- the
//     control plane's ordering is its recommendation, and this package does
//     not re-rank it (SMART_ROUTING.md section 42).
//  5. If nothing survives, the fail history of this attempt is cleared and
//     the filtering runs again against cooldowns only, because a target
//     that failed only has not become healthy just because we ran out of
//     other ideas. If that is still empty, there is truly nothing to try.
func (p *Policy) Next_target(now time.Time, reason Reason, current Target, alternatives []Target) (Target, error) {
	if p.attempts >= p.config.Max_switch_attempts {
		return Target{}, Err_budget_spent
	}

	if reason == Reason_degraded && p.stable_since_set {
		if now.Sub(p.stable_since) < p.config.Min_stable_time {
			return Target{}, Err_too_soon
		}
	}

	if target, ok := p.first_survivor(now, current, alternatives); ok {
		return target, nil
	}

	// Nothing survived with this attempt's fail history in place. Clear it
	// and try again, but only against the cooldowns: those are not a
	// symptom of having run out of ideas, they are a separate fact about
	// the target.
	p.failed = make(map[Target]bool)
	if target, ok := p.first_survivor(now, current, alternatives); ok {
		return target, nil
	}
	return Target{}, Err_no_target
}

// first_survivor walks alternatives in order and returns the first one that
// is not current, not in the fail history and not in cooldown at now.
func (p *Policy) first_survivor(now time.Time, current Target, alternatives []Target) (Target, bool) {
	for _, candidate := range alternatives {
		if candidate == current {
			continue
		}
		if p.failed[candidate] {
			continue
		}
		if until, cooling := p.cooldown_until[candidate]; cooling && now.Before(until) {
			continue
		}
		return candidate, true
	}
	return Target{}, false
}

// Record_failure puts target in the fail history of the current attempt,
// starts its cooldown at now, and raises the attempt counter.
func (p *Policy) Record_failure(now time.Time, target Target) {
	p.failed[target] = true
	p.cooldown_until[target] = now.Add(p.config.Protocol_cooldown)
	p.attempts++
}

// Record_success clears the fail history and the attempt counter, and marks
// now as the moment this connection became stable.
//
// It also drops the cooldown of the target that succeeded, and only that
// one. A cooldown is a prediction that a target is bad; a tunnel that came
// up on it and passed verification is direct evidence against the
// prediction, and keeping the bench warm for something demonstrably working
// would send the next switch somewhere worse. This cannot be used to
// sidestep the cooldown, because reaching it requires actually having
// connected -- the initial target of a connect is chosen by the control
// plane, not by this policy, so it can legitimately be one that is still
// cooling.
//
// Every other cooldown stands: one target working says nothing about the
// rest.
func (p *Policy) Record_success(now time.Time, target Target) {
	p.failed = make(map[Target]bool)
	delete(p.cooldown_until, target)
	p.attempts = 0
	p.stable_since = now
	p.stable_since_set = true
}

// Attempts returns how many switches this attempt has recorded as failures.
func (p *Policy) Attempts() int {
	return p.attempts
}

// Reset returns the policy to its initial state, for a fresh connect: no
// failures, no cooldowns, no recorded stability. Unlike the "nothing left"
// fallback inside Next_target, Reset also drops cooldowns -- a fresh
// connect is a genuinely new attempt to reach the network, not another try
// within the one that just ran out of options.
func (p *Policy) Reset() {
	p.attempts = 0
	p.failed = make(map[Target]bool)
	p.cooldown_until = make(map[Target]time.Time)
	p.stable_since = time.Time{}
	p.stable_since_set = false
}

// Eligible reports local failure and cooldown filtering without spending a switch.
func (p *Policy) Eligible(now time.Time, target Target) bool {
	return !p.failed[target] && !now.Before(p.cooldown_until[target])
}
