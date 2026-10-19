package protocols

import (
	"context"
	"fmt"
	"time"
)

// Connection_config is what a protocol needs to bring up a tunnel. Data is
// bytes the adapter interprets and the core never parses -- the reason this
// type exists at all is so that adding a protocol changes nothing in the
// state machine that drives it.
type Connection_config struct {
	Node_id  string
	Protocol string
	Data     []byte
}

// String prints the node, the protocol and the length of Data, and nothing
// of its contents. Data is key material: it must never appear in an error,
// a log line or a printed struct, and this method exists so that a careless
// %v somewhere later cannot leak a tunnel key.
func (c Connection_config) String() string {
	return fmt.Sprintf("Connection_config{Node_id: %q, Protocol: %q, Data: %d bytes}",
		c.Node_id, c.Protocol, len(c.Data))
}

// Status is what a running protocol reports about itself right now.
type Status struct {
	Running bool
	Since   time.Time
	Detail  string
}

// Protocol is what a tunnel implementation must provide. The state machine
// drives one of these without ever learning which protocol it is.
type Protocol interface {
	Name() string
	Connect(ctx context.Context, config Connection_config) error
	Disconnect(ctx context.Context) error
	Status() Status
}

// Repinner is optional: a protocol that pins its own transport route outside
// the tunnel implements it so the route can follow a network change without
// the tunnel being torn down. Repin with no tunnel up returns nil.
type Repinner interface {
	Repin(ctx context.Context) error
}

// Manager hands out a Protocol by name, and lists what is available.
type Manager interface {
	Get(name string) (Protocol, error)
	Available() []Protocol
}
