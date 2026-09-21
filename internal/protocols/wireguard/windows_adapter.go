//go:build windows

package wireguard

import (
	platform_windows "singularity-vpn-app/internal/platform/windows"
)

// New_windows_adapter wires the real tunnel factory, resolver and network
// together. Nothing outside this function needs to know how to build a
// wireguard-go device, a DNS resolver or platform.Network -- only that
// Adapter needs the three.
func New_windows_adapter(name string) (*Adapter, error) {
	return New_adapter(name, New_windows_tunnel, Default_resolver{}, platform_windows.New_network())
}
