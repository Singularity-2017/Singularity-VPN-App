package protocols

import (
	"context"
	"fmt"
	"time"

	"singularity-vpn-app/internal/games"
)

// Connection_config is what a protocol needs to bring up a tunnel. Data is
// bytes the adapter interprets and the core never parses -- the reason this
// type exists at all is so that adding a protocol changes nothing in the
// state machine that drives it.
type Connection_config struct {
	Node_id  string
	Protocol string
	Data     []byte
	Split    Split
}

// Split says what the tunnel carries. The zero value is the whole machine,
// with the packet driver left as it is.
type Split struct {
	// Games, when not empty, puts the tunnel in games-only mode: only these
	// games' processes go through it, everything else goes direct.
	Games []games.Game
	// Driver_forbidden says a selected game's anti-cheat reacts to the
	// packet driver: it must be taken off the machine, and the tunnel
	// carries the whole machine instead.
	Driver_forbidden bool
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

// Hand_over is optional: a protocol whose tunnel can carry straight on into
// the next connection when that one runs on the same adapter, instead of
// going down and coming back up. Every network change is something each
// program on the machine reacts to by reading its adapters again; on
// Windows 11 26H2 the DNS Client answers those one at a time, and after a
// server switch a dozen browsers doing so kept it on most cores for a
// minute or two (2026-10-29). A tunnel handed over keeps its adapter up,
// and the next Connect changes on the machine only what differs: the
// endpoint's host route, an address when the node gives another.
type Hand_over interface {
	// Carries reports whether next runs on this protocol's adapter.
	Carries(next Protocol) bool
	// Hand_over ends the connection and leaves its adapter, addresses,
	// routes and resolvers in place for the next Connect of a protocol it
	// Carries to take up. The release it returns takes down whatever that
	// Connect did not take up: nothing when it did, everything when it
	// failed or never came. Release is safe to call more than once; with
	// nothing connected, there is nothing to hand over or release.
	Hand_over(ctx context.Context) (release func(context.Context) error)
}

// Transport_reporter is implemented by a protocol that can say whether its
// transport reached the far end at all -- a handshake, bytes each way --
// so a failed verification can tell "nothing reaches the node" from "the
// node answers but traffic does not get through". The report names no
// key, no address and nothing else secret.
type Transport_reporter interface {
	Transport_report() string
}

// Connect_budgeter is implemented by a protocol whose Connect needs longer
// than the state machine's default per-protocol limit: one that tries
// several transports in turn before any of them answers. The whole connect
// keeps its own limit; this only lifts the per-protocol one.
type Connect_budgeter interface {
	Connect_budget() time.Duration
}

// Whole_machine is implemented by a protocol that carries the whole
// machine whatever Connection_config.Split asks: it cannot put the games
// through the tunnel alone.
type Whole_machine interface {
	Whole_machine_only()
}

// Manager hands out a Protocol by name, and lists what is available.
type Manager interface {
	Get(name string) (Protocol, error)
	Available() []Protocol
}

// Path_failure is reserved for transport failures after local setup succeeded.
// Configuration, authentication, adapter and route errors must not use it.
type Path_failure struct {
	Reason string
	Err    error
}

func (e *Path_failure) Error() string { return e.Err.Error() }
func (e *Path_failure) Unwrap() error { return e.Err }
