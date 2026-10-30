package recovery

import "testing"

func Test_after_a_failure_the_next_target_differs_in_server_and_protocol(t *testing.T) {
	policy := New_policy(Default_config())
	failed := Target{Node_id: "de", Protocol: "wireguard"}
	policy.Record_failure(epoch, failed)

	same_node := Target{Node_id: "de", Protocol: "openvpn"}
	same_protocol := Target{Node_id: "nl", Protocol: "wireguard"}
	unlike := Target{Node_id: "fi", Protocol: "amneziawg"}
	got, err := policy.Next_target(epoch, Reason_hard_fail, failed, []Target{same_node, same_protocol, unlike})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != unlike {
		t.Fatalf("Next_target() = %v, want %v: a failure could be the server or the protocol, so the next try changes both", got, unlike)
	}
}

func Test_every_server_and_protocol_that_failed_in_this_attempt_is_avoided(t *testing.T) {
	policy := New_policy(Default_config())
	first := Target{Node_id: "de", Protocol: "wireguard"}
	second := Target{Node_id: "nl", Protocol: "openvpn"}
	policy.Record_failure(epoch, first)
	policy.Record_failure(epoch, second)

	failed_server := Target{Node_id: "de", Protocol: "amneziawg"}
	failed_protocol := Target{Node_id: "fi", Protocol: "wireguard"}
	unlike := Target{Node_id: "fi", Protocol: "amneziawg"}
	got, err := policy.Next_target(epoch, Reason_hard_fail, second, []Target{failed_server, failed_protocol, unlike})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != unlike {
		t.Fatalf("Next_target() = %v, want %v", got, unlike)
	}
}

func Test_with_nothing_wholly_unlike_one_change_beats_none_and_order_decides_the_rest(t *testing.T) {
	policy := New_policy(Default_config())
	failed := Target{Node_id: "de", Protocol: "wireguard"}
	policy.Record_failure(epoch, failed)

	other_protocol := Target{Node_id: "de", Protocol: "openvpn"}
	other_server := Target{Node_id: "nl", Protocol: "wireguard"}
	got, err := policy.Next_target(epoch, Reason_hard_fail, failed, []Target{other_protocol, other_server})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != other_protocol {
		t.Fatalf("Next_target() = %v, want %v: equally unlike, the given order decides", got, other_protocol)
	}
}

func Test_a_degraded_connection_moves_away_from_its_server_and_protocol_too(t *testing.T) {
	policy := New_policy(Default_config())
	current := Target{Node_id: "de", Protocol: "wireguard"}

	same_node := Target{Node_id: "de", Protocol: "amneziawg"}
	unlike := Target{Node_id: "nl", Protocol: "amneziawg"}
	got, err := policy.Next_target(epoch, Reason_degraded, current, []Target{same_node, unlike})
	if err != nil {
		t.Fatalf("Next_target() error = %v", err)
	}
	if got != unlike {
		t.Fatalf("Next_target() = %v, want %v", got, unlike)
	}
}
