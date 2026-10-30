//go:build windows

package wireguard

import (
	"context"

	"singularity-vpn-app/internal/platform"
	platform_windows "singularity-vpn-app/internal/platform/windows"
)

// New_windows_adapter wires the real tunnel factory, resolver and network
// together. Nothing outside this function needs to know how to build a
// wireguard-go device, a DNS resolver or platform.Network -- only that
// Adapter needs the three.
func New_windows_adapter(name string) (*Adapter, error) {
	network := platform_windows.New_network()
	a, err := New_adapter(name, New_windows_tunnel, Default_resolver{}, network)
	if err != nil {
		return nil, err
	}
	a.end_session = kept.end
	// A session starts on an adapter whose state nobody knows -- the first
	// in this process, or one after the last was lost -- so Clear takes
	// everything off it, and the next Apply starts from nothing rather than
	// from a plan the adapter no longer carries.
	kept.mu.Lock()
	kept.clear = func(adapter wintun_adapter) {
		_ = network.Clear(context.Background(), platform.Tunnel_handle(adapter.LUID()))
	}
	kept.mu.Unlock()
	return a, nil
}
