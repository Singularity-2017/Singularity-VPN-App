package state

import "time"

// State is what the application remembers between runs: the profile it
// created, the node and protocol it last connected with, the settings the
// person chose. No credentials here, ever -- a session token, a device
// token, a password have no business in this type; they belong to security,
// and that is the invariant a future field will be tempted to break.
type State struct {
	Version          int
	Client_id        string
	Device_id        string
	Last_node_id     string
	Last_protocol    string
	Connect_on_start bool
	Updated_at       time.Time
}
