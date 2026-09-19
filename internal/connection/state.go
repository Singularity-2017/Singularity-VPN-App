package connection

// State is one point in the connection lifecycle the interface can show a
// person and this package can reason about.
type State string

const (
	State_disconnected  State = "disconnected"
	State_connecting    State = "connecting"
	State_connected     State = "connected"
	State_degraded      State = "degraded"
	State_switching     State = "switching"
	State_disconnecting State = "disconnecting"

	// State_failed is not in CLIENT_CORE.md section 15 -- it comes from
	// CLIENT_AUTO_SWITCH.md section 10.1, and the two documents disagree on
	// purpose. State_disconnected means nobody has asked for a connection.
	// State_failed means somebody asked, every alternative recovery had was
	// tried, and none of them worked. Collapsing the two would show "off"
	// to a person whose connection just died after three attempts, which
	// hides the one piece of information -- "this did not work, try
	// something else" -- they actually need right then.
	State_failed State = "failed"
)

// State_change is the payload of a Kind_state_changed event: what state the
// connection left, and what it entered. It exists for the transitions that
// have no more specific event of their own -- recovering from degraded back
// to connected, for one -- so that "the state changed" is always observable
// even when nothing else about it is.
type State_change struct {
	From State
	To   State
}

// transitions lists, for each state, every state Allowed permits moving to
// next. CLIENT_CORE.md section 16, as written; every state may also move to
// itself, which Allowed handles without needing it spelled out here.
var transitions = map[State][]State{
	State_disconnected:  {State_connecting},
	State_connecting:    {State_connected, State_disconnected, State_failed},
	State_connected:     {State_degraded, State_switching, State_disconnecting},
	State_degraded:      {State_connected, State_switching, State_disconnecting},
	State_switching:     {State_connected, State_failed, State_disconnecting},
	State_disconnecting: {State_disconnected},
	State_failed:        {State_connecting, State_disconnected},
}

// Allowed reports whether the machine may move from one state to another.
// A state may always move to itself: that is a no-op, not a transition, and
// callers rely on it costing nothing and announcing nothing.
func Allowed(from, to State) bool {
	if from == to {
		return true
	}
	for _, candidate := range transitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}
