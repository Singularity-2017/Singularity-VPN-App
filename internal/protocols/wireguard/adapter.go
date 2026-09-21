package wireguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"singularity-vpn-app/internal/platform"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/routing"
)

// tunnel is the embedded wireguard-go device, behind the four things this
// adapter asks of it. device_windows.go wires the real one; every test in
// this package wires a fake.
type tunnel interface {
	Set_config(uapi string) error
	Up() error
	// Close reports an error so a fake can exercise the unwind path that
	// assumes it might fail. The real wireguard-go (*device.Device).Close
	// returns nothing; its wrapper always reports nil.
	Close() error
	Handle() platform.Tunnel_handle
}

// resolver turns the "host:port" a peer's Endpoint carries into an address.
// It is its own seam, separate from tunnel, because it must run exactly
// once and before the tunnel comes up -- see the comment on Uapi_set in
// uapi.go, and amendment 1 in STAGE3B.md: routing.Plan_for refuses a Tunnel
// whose Endpoint was never resolved, so a resolve failure has to stop
// Connect before anything else exists.
type resolver interface {
	Resolve(ctx context.Context, host_port string) (netip.AddrPort, error)
}

// tunnel_factory creates the tunnel device for one connection attempt,
// sized and named for what the parsed config asked for. It is a seam, not a
// stored dependency, because a tunnel exists only for the lifetime of one
// connection: New_windows_tunnel is the real one, New_adapter's caller
// supplies a fake in every test.
type tunnel_factory func(name string, mtu int) (tunnel, error)

// Err_already_connected is what Connect returns when the adapter already
// has a tunnel up. Connect must not touch that tunnel to discover this: the
// check happens before anything else runs.
var Err_already_connected = errors.New("wireguard: already connected")

// Err_multiple_peers_unsupported is what Connect returns for a config with
// more than one [Peer] section. Parse accepts any number of peers, the way
// wg-quick does, but internal/routing.Tunnel carries a single Endpoint and
// a single set of allowed prefixes fed to Plan_for: there is no defined way
// today to turn a multi-peer config into one machine-wide routing.Plan, and
// guessing -- for instance, using only the first peer's endpoint for the
// split-tunnel host-route exclusion while silently ignoring the others --
// would leave later peers' endpoints unprotected against being swallowed by
// their own allowed prefixes. This client's real configs come from a single
// control-plane node and always carry exactly one peer; refusing here is
// louder than a routing bug nobody could see coming.
var Err_multiple_peers_unsupported = errors.New("wireguard: this adapter's routing plan supports exactly one peer")

// Adapter implements protocols.Protocol for WireGuard. It holds no key
// material of its own between calls: everything it needs comes from the
// Connection_config passed to Connect and lives only as long as the parsed
// Tunnel_config used to build the running tunnel.
type Adapter struct {
	name       string
	new_tunnel tunnel_factory
	resolve    resolver
	network    platform.Network
	clock      func() time.Time

	mu      sync.Mutex
	running bool
	since   time.Time
	detail  string
	handle  platform.Tunnel_handle
	device  tunnel
}

// New_adapter builds a WireGuard Adapter over the three seams above plus
// the adapter name the OS will see. New_windows_adapter (windows_adapter.go)
// wires the real three; every other caller of this constructor is a test.
func New_adapter(name string, new_tunnel tunnel_factory, resolve resolver, network platform.Network) (*Adapter, error) {
	switch {
	case name == "":
		return nil, fmt.Errorf("wireguard: adapter name is required")
	case new_tunnel == nil:
		return nil, fmt.Errorf("wireguard: tunnel factory is required")
	case resolve == nil:
		return nil, fmt.Errorf("wireguard: resolver is required")
	case network == nil:
		return nil, fmt.Errorf("wireguard: network is required")
	}
	return &Adapter{
		name:       name,
		new_tunnel: new_tunnel,
		resolve:    resolve,
		network:    network,
		clock:      time.Now,
	}, nil
}

// Name returns "wireguard", the name this adapter is registered under.
func (a *Adapter) Name() string {
	return "wireguard"
}

// Connect parses config, resolves the peer's endpoint, brings up a tunnel
// device, applies its routing.Plan and starts it -- in that order, as six
// steps:
//
//  1. Parse. A parse failure returns before anything exists.
//  2. Resolve the peer's endpoint. A resolve failure returns before
//     anything exists.
//  3. Create the tunnel device.
//  4. Set_config with the UAPI string.
//  5. Compute the routing.Plan and Apply it.
//  6. Up.
//
// Any failure from step 3 onwards unwinds through unwind: both the network
// state Apply may have set and the tunnel device Close, attempted
// regardless of whether either was actually reached, because platform.Network
// Clear and this package's Close wrapper are both defined to be safe on
// something that was never configured. A half-built tunnel -- a device with
// no routing behind it, or routes with no device answering them -- is worse
// than none, so nothing here is left half done.
func (a *Adapter) Connect(ctx context.Context, config protocols.Connection_config) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return Err_already_connected
	}

	parsed, err := Parse(string(config.Data))
	if err != nil {
		return fmt.Errorf("wireguard: %w", err)
	}
	if len(parsed.Peers) != 1 {
		return fmt.Errorf("%w: got %d", Err_multiple_peers_unsupported, len(parsed.Peers))
	}
	peer := parsed.Peers[0]

	endpoint, err := a.resolve.Resolve(ctx, peer.Endpoint)
	if err != nil {
		return fmt.Errorf("wireguard: resolving peer endpoint: %w", err)
	}

	device, err := a.new_tunnel(a.name, parsed.Mtu)
	if err != nil {
		return fmt.Errorf("wireguard: creating tunnel device: %w", err)
	}
	handle := device.Handle()

	uapi, err := parsed.Uapi_set([]netip.AddrPort{endpoint})
	if err != nil {
		return a.unwind(ctx, device, handle, err)
	}
	if err := device.Set_config(uapi); err != nil {
		// err is what wireguard-go's IpcSet reported, never uapi itself:
		// uapi carries the private key in hex. See amendment 3 in
		// STAGE3B.md.
		return a.unwind(ctx, device, handle, fmt.Errorf("setting device configuration: %w", err))
	}

	plan, err := routing.Plan_for(routing.Tunnel{
		Addresses: parsed.Addresses,
		Allowed:   peer.Allowed_ips,
		Dns:       parsed.Dns,
		Mtu:       parsed.Mtu,
		Endpoint:  endpoint,
	})
	if err != nil {
		return a.unwind(ctx, device, handle, err)
	}

	if err := a.network.Apply(ctx, handle, plan); err != nil {
		return a.unwind(ctx, device, handle, fmt.Errorf("applying routing plan: %w", err))
	}

	if err := device.Up(); err != nil {
		return a.unwind(ctx, device, handle, fmt.Errorf("bringing tunnel up: %w", err))
	}

	a.device = device
	a.handle = handle
	a.running = true
	a.since = a.clock()
	a.detail = fmt.Sprintf("connected, %d peer(s)", len(parsed.Peers))
	return nil
}

// unwind tears down what Connect built from step 3 onward: platform.Network
// state and the tunnel device, both attempted even though at most one of
// them may actually have anything to undo. cause is the failure that
// triggered the unwind; it and any error from the two teardown calls are
// joined, so a person sees not just what failed but whether cleaning up
// after it failed too.
func (a *Adapter) unwind(ctx context.Context, device tunnel, handle platform.Tunnel_handle, cause error) error {
	clear_err := a.network.Clear(ctx, handle)
	close_err := device.Close()
	return errors.Join(fmt.Errorf("wireguard: %w", cause), clear_err, close_err)
}

// Disconnect clears the routing.Plan and closes the tunnel device, both
// attempted even if the first fails, errors joined -- the same reasoning as
// platform.Network.Clear: this is the teardown path, and stopping half way
// through leaves a machine in a state nobody chose. Disconnecting an adapter
// that is not connected is not an error and touches nothing.
func (a *Adapter) Disconnect(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.running {
		return nil
	}

	clear_err := a.network.Clear(ctx, a.handle)
	close_err := a.device.Close()

	a.running = false
	a.device = nil
	a.handle = 0
	a.detail = ""
	// since is cleared with the rest. A time left over from a tunnel that
	// is no longer up is not a measurement of anything, and Status would
	// otherwise report "not running" beside the moment it has been running
	// since -- see the project rule that a number is either real or absent.
	a.since = time.Time{}

	if clear_err != nil {
		clear_err = fmt.Errorf("wireguard: clearing routing plan: %w", clear_err)
	}
	if close_err != nil {
		close_err = fmt.Errorf("wireguard: closing tunnel device: %w", close_err)
	}
	return errors.Join(clear_err, close_err)
}

// Status reports whether the tunnel is running, when it came up, and a
// detail string that names nothing secret -- see protocols.Status and
// protocols.Connection_config.String for why that must hold.
func (a *Adapter) Status() protocols.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return protocols.Status{Running: a.running, Since: a.since, Detail: a.detail}
}

// Default_resolver resolves an endpoint's "host:port" with the standard
// library resolver. It is the real implementation of resolver: not
// Windows-specific, since hostname resolution needs no operating-system
// privilege, so New_windows_adapter wires it in directly and cmd/svpn-probe
// uses it to print what an endpoint resolves to before anything is brought
// up. lookup defaults to net.DefaultResolver.LookupNetIP and is a field
// only so a test can replace it without a real DNS query.
type Default_resolver struct {
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (r Default_resolver) Resolve(ctx context.Context, host_port string) (netip.AddrPort, error) {
	host, port_text, err := net.SplitHostPort(host_port)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("wireguard: %q is not host:port: %w", host_port, err)
	}
	port, err := strconv.Atoi(port_text)
	if err != nil || port < 1 || port > 65535 {
		return netip.AddrPort{}, fmt.Errorf("wireguard: %q has an invalid port", host_port)
	}

	lookup := r.lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("wireguard: resolving %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return netip.AddrPort{}, fmt.Errorf("wireguard: %q did not resolve to any address", host)
	}
	return netip.AddrPortFrom(addrs[0].Unmap(), uint16(port)), nil
}
