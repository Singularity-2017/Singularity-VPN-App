package platform

import (
	"net"
	"strings"
)

// Vpn_conflict describes another active VPN or tunnel interface that can
// intercept routes, DNS, or WFP sockets and disrupt Singularity VPN.
type Vpn_conflict struct {
	Adapter string
	Name    string
}

var known_vpn_adapters = []struct {
	needle string
	name   string
}{
	{"happ", "Happ"},
	{"sing-tun", "sing-box"},
	{"singbox", "sing-box"},
	{"hiddify", "Hiddify"},
	{"nekoray", "NekoRay"},
	{"nekobox", "NekoBox"},
	{"v2ray", "v2rayN"},
	{"amnezia", "AmneziaVPN"},
	{"outline", "Outline"},
	{"clash", "Clash"},
	{"mihomo", "Mihomo"},
	{"proton", "Proton VPN"},
	{"mullvad", "Mullvad VPN"},
	{"nordlynx", "NordVPN"},
	{"surfshark", "Surfshark"},
	{"windscribe", "Windscribe"},
	{"adguard", "AdGuard VPN"},
}

// Detect_vpn_conflict checks active network interfaces for a running competing
// VPN tunnel. Returns nil when none is active or interfaces cannot be read.
func Detect_vpn_conflict() *Vpn_conflict {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	return Detect_vpn_conflict_from(ifaces)
}

// Detect_vpn_conflict_from inspects a list of interfaces; pure for tests.
func Detect_vpn_conflict_from(ifaces []net.Interface) *Vpn_conflict {
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		lower := strings.ToLower(iface.Name)
		if strings.Contains(lower, "singularity") {
			continue
		}
		for _, known := range known_vpn_adapters {
			if strings.Contains(lower, known.needle) {
				return &Vpn_conflict{Adapter: iface.Name, Name: known.name}
			}
		}
	}
	return nil
}
