package recovery

import (
	"testing"
	"time"
)

func Test_eligible_respects_local_failures_without_spending_budget(t *testing.T) {
	p := New_policy(Default_config())
	now := time.Now()
	target := Target{Node_id: "de", Protocol: "amneziawg"}
	if !p.Eligible(now, target) {
		t.Fatal("new target ineligible")
	}
	p.Record_failure(now, target)
	if p.Eligible(now, target) {
		t.Fatal("failed target eligible")
	}
	before := p.Attempts()
	p.Eligible(now, target)
	if p.Attempts() != before {
		t.Fatal("eligibility spent budget")
	}
	p.Record_success(now, Target{Node_id: "de", Protocol: "wireguard"})
	if p.Eligible(now, target) {
		t.Fatal("another target success erased cooldown")
	}
	if !p.Eligible(now.Add(Default_config().Protocol_cooldown), target) {
		t.Fatal("expired cooldown active")
	}
}
