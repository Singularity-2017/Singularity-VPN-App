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

	// Repin moves every host route Apply pinned for this handle onto the
	// path the destination would take now, outside the tunnel. It is for a
	// network change while the tunnel is up (another adapter, a new
	// gateway): the new pin is added before the old one is removed, so the
	// tunnel's own transport is never without a route and never falls into
	// the tunnel. A handle with no pins is a no-op. If a destination has no
	// route outside the tunnel the old pin is kept and an error says so.
	Repin(ctx context.Context, handle Tunnel_handle) error
}
