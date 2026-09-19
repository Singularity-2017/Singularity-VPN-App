package events

import "time"

// Kind names one thing that happened to the connection.
type Kind string

const (
	Kind_state_changed          Kind = "state_changed"
	Kind_connection_started     Kind = "connection_started"
	Kind_connection_established Kind = "connection_established"
	Kind_connection_lost        Kind = "connection_lost"
	Kind_connection_degraded    Kind = "connection_degraded"
	Kind_switch_started         Kind = "switch_started"
	Kind_switch_completed       Kind = "switch_completed"
	Kind_switch_failed          Kind = "switch_failed"
	Kind_diagnostic_completed   Kind = "diagnostic_completed"
	Kind_better_option          Kind = "better_option_available"
	Kind_error                  Kind = "error"
)

// Event is one thing that happened, timestamped and tied to the session it
// happened in. Data carries whatever detail is specific to Kind; a listener
// that does not recognize the kind can still show it and the time.
type Event struct {
	Kind       Kind
	At         time.Time
	Session_id string
	Data       any
}
