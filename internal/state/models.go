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
	Strategy         Strategy
	// Games are the catalog ids of the games the person wants carried
	// through the tunnel, in the order they chose them.
	Games      []string
	Updated_at time.Time
}

// Strategy is what the person said the connection is for. The control
// plane makes the choice; this is only what it is asked to choose for. The
// zero value asks for nothing in particular, which is the default: the
// control plane picks the least loaded server and the protocol that gets
// through.
type Strategy struct {
	// Anonymity is "" (off) or light, standard or high.
	Anonymity string
	Bypass    bool
	// Country is an ISO 3166-1 alpha-2 code to be seen from, or "" for
	// anywhere.
	Country string
	Route   bool
	// Node_id is a server the person picked by hand, or "" for the control
	// plane to pick -- which is automatic server selection, and what moves
	// a connection off a node that dies.
	Node_id string
}
