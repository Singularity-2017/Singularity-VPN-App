package connection

import "testing"

func Test_allowed_matches_the_transition_table(t *testing.T) {
	table := map[State][]State{
		State_disconnected:  {State_connecting},
		State_connecting:    {State_connected, State_disconnected, State_failed},
		State_connected:     {State_degraded, State_switching, State_disconnecting},
		State_degraded:      {State_connected, State_switching, State_disconnecting},
		State_switching:     {State_connected, State_failed, State_disconnecting},
		State_disconnecting: {State_disconnected},
		State_failed:        {State_connecting, State_disconnected},
	}

	all := []State{
		State_disconnected, State_connecting, State_connected, State_degraded,
		State_switching, State_disconnecting, State_failed,
	}

	for _, from := range all {
		allowed := make(map[State]bool, len(table[from])+1)
		allowed[from] = true // every state may move to itself
		for _, to := range table[from] {
			allowed[to] = true
		}

		for _, to := range all {
			want := allowed[to]
			got := Allowed(from, to)
			if got != want {
				t.Errorf("Allowed(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func Test_every_state_may_move_to_itself(t *testing.T) {
	all := []State{
		State_disconnected, State_connecting, State_connected, State_degraded,
		State_switching, State_disconnecting, State_failed,
	}
	for _, s := range all {
		if !Allowed(s, s) {
			t.Errorf("Allowed(%s, %s) = false, want true: a state must always be able to stay put", s, s)
		}
	}
}
