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

	"github.com/amnezia-vpn/amneziawg-go/tun"

	"singularity-vpn-app/internal/games"
	"singularity-vpn-app/internal/platform"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/routing"
	"singularity-vpn-app/internal/split"
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
	// Stats is the peer's transport counters. The real device reads them
	// from its UAPI and keeps nothing else of what that returns: the same
	// text carries the private key.
	Stats() (Transport_stats, error)
}

// Transport_stats is what the device knows about reaching its peer.
type Transport_stats struct {
	Last_handshake time.Time // zero: none yet
	Sent           uint64
	Received       uint64
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
//
// wrap, when not nil, is applied to the tun device before wireguard-go is
// given it: games-only mode puts split_tun between them.
type tunnel_factory func(name string, mtu int, wrap device_wrapper) (tunnel, error)

type device_wrapper func(tun.Device) tun.Device

// Game_router is the platform's half of games-only mode: it finds the
// selected games' processes, captures their outbound packets and puts the
// replies back on the interface they came from. The Windows one sits on
// the WinDivert driver.
type Game_router interface {
	// Start captures the games' packets. Each is passed through splitter
	// and, when it took it, handed to inject; one it did not take goes
	// out unchanged. Start returns once capture is running.
	Start(selected []games.Game, splitter *split.Splitter, inject func(packet []byte) bool) error
	// Running_games names the selected games whose processes hold a
	// port now.
	Running_games() []string
	// Deliver injects a reply splitter has already rewritten, inbound on
	// origin's interface. It must not keep packet.
	Deliver(packet []byte, origin split.Origin) bool
	// Stop ends capture and closes the driver's handles. The driver stays
	// installed; Remove_driver takes it away.
	Stop() error
	// Remove_driver takes the packet driver off the machine if this
	// program installed it.
	Remove_driver() error
}

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

	// router and probe_routes are set by Enable_games; without a router
	// a games-only connect is refused rather than silently carrying the
	// whole machine.
	router       Game_router
	probe_routes []netip.Prefix

	mu      sync.Mutex
	running bool
	since   time.Time
	detail  string
	handle  platform.Tunnel_handle
	device  tunnel
	routing bool // the router is capturing for this tunnel
	probe   string
	// game_path is the games-only tunnel's path, for Ping_game.
	game_path *game_path
}

// Enable_games gives the adapter the platform's game router and the
// destinations the service itself sends through the tunnel in games-only
// mode -- the probe target, without which the probe would measure the
// direct path and call any tunnel healthy. Call it before the first
// Connect.
func (a *Adapter) Enable_games(router Game_router, probe_routes []netip.Prefix) {
	a.router = router
	a.probe_routes = probe_routes
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

// Amnezia is the same adapter registered under "amneziawg". The library is
// amneziawg-go, which speaks plain WireGuard when a config carries no
// obfuscation parameters and AmneziaWG when it does, so one device, one
// router and one running state serve both names -- and only one tunnel can
// ever be up, whichever name brought it.
type Amnezia struct {
	*Adapter
}

// Name returns "amneziawg".
func (Amnezia) Name() string {
	return "amneziawg"
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

	a.detail = ""
	if config.Split.Driver_forbidden && a.router != nil {
		// A game whose anti-cheat reacts to the driver is selected: the
		// driver goes, and the tunnel carries the whole machine. A
		// failure here must not keep the person offline, so it is noted
		// in the detail rather than returned.
		if err := a.router.Remove_driver(); err != nil {
			a.detail = fmt.Sprintf("the packet driver could not be removed: %v", err)
		}
	}

	games_only := len(config.Split.Games) > 0
	var splitter *split.Splitter
	var path *game_path
	var wrapped *split_tun
	var wrap device_wrapper
	if games_only {
		if a.router == nil {
			return fmt.Errorf("wireguard: games-only mode needs the packet driver, and this build has none")
		}
		tunnel_address, ok := first_ipv4(parsed.Addresses)
		if !ok {
			return fmt.Errorf("wireguard: games-only mode needs an IPv4 tunnel address, and the config has none")
		}
		splitter = split.New_splitter(tunnel_address, parsed.Mtu)
		path = new_game_path(splitter)
		for _, address := range parsed.Dns {
			if address.Is4() {
				path.control = address
				break
			}
		}
		router := a.router
		wrap = func(inner tun.Device) tun.Device {
			wrapped = new_split_tun(inner, func(packet []byte) bool {
				if path.take_reply(packet) {
					return true
				}
				origin, ok := splitter.Inbound(packet)
				if !ok {
					return false
				}
				router.Deliver(packet, origin)
				return true
			})
			return wrapped
		}
	}

	device, err := a.new_tunnel(a.name, parsed.Mtu, wrap)
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
		Only:      a.only_routes(games_only),
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

	if games_only {
		if wrapped == nil {
			return a.unwind(ctx, device, handle, errors.New("the tunnel device was built without the games-only wrapper"))
		}
		if err := a.router.Start(config.Split.Games, splitter, wrapped.Inject); err != nil {
			return a.unwind(ctx, device, handle, fmt.Errorf("starting the game router: %w", err))
		}
		path.inject = wrapped.Inject
	}

	a.device = device
	a.handle = handle
	a.running = true
	a.routing = games_only
	a.game_path = path
	a.probe = split_probe_target(parsed.Dns, peer.Allowed_ips, games_only)
	a.since = a.clock()
	switch {
	case games_only:
		a.detail = fmt.Sprintf("connected, %d peer(s), games only (%d)", len(parsed.Peers), len(config.Split.Games))
	case a.detail == "":
		a.detail = fmt.Sprintf("connected, %d peer(s)", len(parsed.Peers))
	}
	return nil
}

// Probe_target is what a health probe should dial through this tunnel when
// the static target would be dropped: the node's resolver, as "addr:53".
// It is empty when the tunnel is not running, when it carries everything
// (the static target is reachable), in games-only mode, and when the config
// names no DNS address the tunnel carries.
func (a *Adapter) Probe_target() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return ""
	}
	return a.probe
}

// split_probe_target picks the probe target for a tunnel that carries only
// part of the traffic: the first DNS address of the config that lies inside
// one of the peer's AllowedIPs. TCP port 53 is used because the AntiZapret
// resolver answers TCP.
func split_probe_target(dns []netip.Addr, allowed []netip.Prefix, games_only bool) string {
	if games_only {
		return ""
	}
	for _, prefix := range allowed {
		if prefix.Bits() == 0 {
			return ""
		}
	}
	for _, address := range dns {
		for _, prefix := range allowed {
			if prefix.Contains(address) {
				return net.JoinHostPort(address.String(), "53")
			}
		}
	}
	return ""
}

// only_routes is routing.Tunnel.Only for this connect: nil for the whole
// machine, the probe routes (never nil) in games-only mode.
func (a *Adapter) only_routes(games_only bool) []netip.Prefix {
	if !games_only {
		return nil
	}
	return append([]netip.Prefix{}, a.probe_routes...)
}

func first_ipv4(prefixes []netip.Prefix) (netip.Addr, bool) {
	for _, prefix := range prefixes {
		if prefix.Addr().Is4() {
			return prefix.Addr(), true
		}
	}
	return netip.Addr{}, false
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

	var stop_err error
	if a.routing {
		// Capture stops before the device closes, so no game packet is
		// handed to a tunnel that is going away.
		if err := a.router.Stop(); err != nil {
			stop_err = fmt.Errorf("wireguard: stopping the game router: %w", err)
		}
	}
	clear_err := a.network.Clear(ctx, a.handle)
	close_err := a.device.Close()

	a.running = false
	a.routing = false
	a.game_path = nil
	a.probe = ""
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
	return errors.Join(stop_err, clear_err, close_err)
}

// Status reports whether the tunnel is running, when it came up, and a
// detail string that names nothing secret -- see protocols.Status and
// protocols.Connection_config.String for why that must hold.
func (a *Adapter) Status() protocols.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return protocols.Status{Running: a.running, Since: a.since, Detail: a.detail}
}

// Transport_report implements protocols.Transport_reporter: whether a
// handshake with the node happened and how many bytes went each way.
func (a *Adapter) Transport_report() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.device == nil {
		return ""
	}
	stats, err := a.device.Stats()
	if err != nil {
		return ""
	}
	return transport_report(stats, a.clock())
}

func transport_report(stats Transport_stats, now time.Time) string {
	if stats.Last_handshake.IsZero() {
		return fmt.Sprintf("no handshake with the node; sent %d bytes, received %d", stats.Sent, stats.Received)
	}
	return fmt.Sprintf("handshake %s ago; sent %d bytes, received %d",
		now.Sub(stats.Last_handshake).Round(time.Second), stats.Sent, stats.Received)
}

// Repin implements protocols.Repinner: it moves the endpoint host route onto
// the path the network offers now. With no tunnel up there is nothing pinned
// and it returns nil.
func (a *Adapter) Repin(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.running {
		return nil
	}
	if err := a.network.Repin(ctx, a.handle); err != nil {
		return fmt.Errorf("wireguard: repinning the endpoint route: %w", err)
	}
	return nil
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
