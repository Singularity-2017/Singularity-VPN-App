package recovery

import (
	"errors"
	"testing"
	"time"
)

var (
	target_de_01 = Target{Node_id: "de-01", Protocol: "wireguard"}
	target_nl_02 = Target{Node_id: "nl-02", Protocol: "wireguard"}
	target_fr_03 = Target{Node_id: "fr-03", Protocol: "openvpn"}
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func Test_the_first_call_with_no_history_returns_the_first_alternative(t *testing.T) {
	policy := New_policy(Default_config())

	got, err := policy.Next_target(epoch, Reason_hard_fail, target_de_01, []Target{target_nl_02, target_fr_03})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != target_nl_02 {
		t.Fatalf("Next_target() = %v, want %v", got, target_nl_02)
	}
}

func Test_the_current_target_is_never_returned(t *testing.T) {
	policy := New_policy(Default_config())

	got, err := policy.Next_target(epoch, Reason_hard_fail, target_de_01, []Target{target_de_01, target_nl_02})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != target_nl_02 {
		t.Fatalf("Next_target() = %v, want %v (current excluded)", got, target_nl_02)
	}
}

func Test_a_target_in_the_fail_history_is_skipped(t *testing.T) {
	policy := New_policy(Default_config())
	policy.Record_failure(epoch, target_nl_02)

	got, err := policy.Next_target(epoch, Reason_hard_fail, target_de_01, []Target{target_nl_02, target_fr_03})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != target_fr_03 {
		t.Fatalf("Next_target() = %v, want %v (failed target skipped)", got, target_fr_03)
	}
}

func Test_a_target_in_cooldown_is_skipped_and_offered_again_once_it_expires(t *testing.T) {
	config := Default_config()
	policy := New_policy(config)
	policy.Record_failure(epoch, target_nl_02)

	still_cooling := epoch.Add(config.Protocol_cooldown - time.Second)
	got, err := policy.Next_target(still_cooling, Reason_hard_fail, target_de_01, []Target{target_nl_02, target_fr_03})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != target_fr_03 {
		t.Fatalf("Next_target() = %v, want %v while nl-02 is still cooling down", got, target_fr_03)
	}

	expired := epoch.Add(config.Protocol_cooldown + time.Second)
	got, err = policy.Next_target(expired, Reason_hard_fail, target_de_01, []Target{target_nl_02})
	if err != nil {
		t.Fatalf("Next_target() error = %v, want nl-02 available once its cooldown expired", err)
	}
	if got != target_nl_02 {
		t.Fatalf("Next_target() = %v, want %v once the cooldown expired", got, target_nl_02)
	}
}

func Test_the_cooldown_is_per_target_not_per_protocol(t *testing.T) {
	policy := New_policy(Default_config())
	policy.Record_failure(epoch, target_de_01) // wireguard on de-01 fails

	// wireguard on nl-02 is a different target and must still be offered.
	got, err := policy.Next_target(epoch, Reason_hard_fail, target_fr_03, []Target{target_de_01, target_nl_02})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != target_nl_02 {
		t.Fatalf("Next_target() = %v, want %v: the cooldown must not follow the protocol", got, target_nl_02)
	}
}

func Test_the_budget_refuses_a_fourth_attempt(t *testing.T) {
	config := Default_config()
	policy := New_policy(config)

	cooldown := config.Protocol_cooldown + time.Second
	now := epoch
	for i := 0; i < config.Max_switch_attempts; i++ {
		_, err := policy.Next_target(now, Reason_hard_fail, target_de_01, []Target{target_nl_02})
		if err != nil {
			t.Fatalf("Next_target() attempt %d error = %v", i+1, err)
		}
		policy.Record_failure(now, target_nl_02)
		now = now.Add(cooldown)
	}

	_, err := policy.Next_target(now, Reason_hard_fail, target_de_01, []Target{target_nl_02})
	if !errors.Is(err, Err_budget_spent) {
		t.Fatalf("Next_target() error = %v, want Err_budget_spent", err)
	}
}

func Test_record_success_clears_the_history_and_the_budget(t *testing.T) {
	policy := New_policy(Default_config())
	policy.Record_failure(epoch, target_nl_02)
	if policy.Attempts() != 1 {
		t.Fatalf("Attempts() = %d, want 1 after one failure", policy.Attempts())
	}

	policy.Record_success(epoch, target_de_01)
	if policy.Attempts() != 0 {
		t.Fatalf("Attempts() = %d, want 0 after a success", policy.Attempts())
	}

	// nl-02's fail history is cleared, so it becomes eligible again
	// immediately (its cooldown from the earlier failure has since expired).
	config := Default_config()
	later := epoch.Add(config.Protocol_cooldown + time.Second)
	got, err := policy.Next_target(later, Reason_hard_fail, target_de_01, []Target{target_nl_02})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != target_nl_02 {
		t.Fatalf("Next_target() = %v, want %v", got, target_nl_02)
	}
}

func Test_a_degraded_switch_inside_min_stable_time_is_err_too_soon(t *testing.T) {
	config := Default_config()
	policy := New_policy(config)
	policy.Record_success(epoch, target_de_01)

	soon := epoch.Add(config.Min_stable_time - time.Second)
	_, err := policy.Next_target(soon, Reason_degraded, target_de_01, []Target{target_nl_02})
	if !errors.Is(err, Err_too_soon) {
		t.Fatalf("Next_target() error = %v, want Err_too_soon", err)
	}
}

func Test_a_hard_fail_switch_inside_the_same_window_is_allowed(t *testing.T) {
	config := Default_config()
	policy := New_policy(config)
	policy.Record_success(epoch, target_de_01)

	soon := epoch.Add(config.Min_stable_time - time.Second)
	got, err := policy.Next_target(soon, Reason_hard_fail, target_de_01, []Target{target_nl_02})
	if err != nil {
		t.Fatalf("Next_target() error = %v, want nil: a hard failure ignores the stability window", err)
	}
	if got != target_nl_02 {
		t.Fatalf("Next_target() = %v, want %v", got, target_nl_02)
	}
}

func Test_a_policy_that_has_never_succeeded_is_never_too_soon(t *testing.T) {
	policy := New_policy(Default_config())

	_, err := policy.Next_target(epoch, Reason_degraded, target_de_01, []Target{target_nl_02})
	if err != nil {
		t.Fatalf("Next_target() error = %v, want nil: nothing has ever been stable yet", err)
	}
}

func Test_when_everything_is_filtered_out_the_fail_history_is_cleared_and_a_target_is_returned(t *testing.T) {
	config := Default_config()
	policy := New_policy(config)
	policy.Record_failure(epoch, target_nl_02)

	// nl-02's cooldown has since expired, but it is still in this attempt's
	// fail history and nl-02 is the only alternative: only the "nothing
	// left" fallback, which clears the fail history, can produce a target.
	expired := epoch.Add(config.Protocol_cooldown + time.Second)
	got, err := policy.Next_target(expired, Reason_hard_fail, target_de_01, []Target{target_nl_02})
	if err != nil {
		t.Fatalf("Next_target() error = %v, want nl-02 recovered once history was cleared", err)
	}
	if got != target_nl_02 {
		t.Fatalf("Next_target() = %v, want %v", got, target_nl_02)
	}
}

func Test_when_everything_is_in_cooldown_the_reset_does_not_rescue_it(t *testing.T) {
	config := Default_config()
	policy := New_policy(config)
	policy.Record_failure(epoch, target_nl_02)

	// Still inside the cooldown: clearing the fail history cannot help,
	// because the filter that remains is the cooldown itself.
	soon := epoch.Add(config.Protocol_cooldown - time.Second)
	_, err := policy.Next_target(soon, Reason_hard_fail, target_de_01, []Target{target_nl_02})
	if !errors.Is(err, Err_no_target) {
		t.Fatalf("Next_target() error = %v, want Err_no_target", err)
	}
}

func Test_reset_restores_the_initial_state(t *testing.T) {
	config := Default_config()
	policy := New_policy(config)
	policy.Record_failure(epoch, target_nl_02)
	policy.Record_success(epoch, target_de_01)

	policy.Reset()

	if policy.Attempts() != 0 {
		t.Fatalf("Attempts() = %d after Reset(), want 0", policy.Attempts())
	}
	// A fresh policy has never recorded a success, so a degraded switch
	// right away must not be too soon.
	_, err := policy.Next_target(epoch, Reason_degraded, target_de_01, []Target{target_fr_03})
	if err != nil {
		t.Fatalf("Next_target() error = %v, want nil after Reset()", err)
	}
}

// A cooldown is a prediction that a target is bad, and a tunnel that came up
// on it is evidence against the prediction. The target that just worked stops
// being benched; every other bench stands, because one target working says
// nothing about the rest.
func Test_a_success_clears_that_targets_cooldown_and_no_other(t *testing.T) {
	config := Default_config()
	policy := New_policy(config)
	policy.Record_failure(epoch, target_de_01)
	policy.Record_failure(epoch, target_nl_02)

	// The control plane, not this policy, picks the target of a connect, so
	// de-01 can be reached again while it is still cooling -- and it works.
	later := epoch.Add(time.Minute)
	policy.Record_success(later, target_de_01)

	got, err := policy.Next_target(later, Reason_hard_fail, target_fr_03,
		[]Target{target_de_01, target_nl_02})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != target_de_01 {
		t.Fatalf("Next_target() = %v, want %v: the target that just worked is still benched", got, target_de_01)
	}

	_, err = policy.Next_target(later, Reason_hard_fail, target_fr_03, []Target{target_nl_02})
	if !errors.Is(err, Err_no_target) {
		t.Fatalf("Next_target() error = %v, want Err_no_target: nl-02's cooldown must survive another target's success", err)
	}
}
