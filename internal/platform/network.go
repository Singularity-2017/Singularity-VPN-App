package platform

import (
	"context"

	"singularity-vpn-app/internal/routing"
)

// Tunnel_handle identifies a tunnel adapter the operating system has created.
// It is deliberately opaque and deliberately not a name: on Windows it is the
// adapter's LUID, which is what the networking API takes, and an adapter can
// be renamed while it is up.
type Tunnel_handle uint64

// Network applies a routing plan to a tunnel adapter and takes it back off.
// Both are privileged operations.
type Network interface {
	Apply(ctx context.Context, handle Tunnel_handle, plan routing.Plan) error

	// Clear must be safe to call on a handle that was never configured, and
	// safe to call twice. It is the teardown path, it runs when something
	// has already gone wrong, and a teardown that fails because the thing
	// it is undoing was never done is a teardown that strands a person's
	// routing table.
	Clear(ctx context.Context, handle Tunnel_handle) error
}
