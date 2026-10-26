package openvpn

import (
	"net"
	"net/netip"
	"strings"
)

// carried is what the tunnel takes, as far as the profile and the node's
// PUSH_REPLY have said: the whole machine (redirect-gateway), or only the
// routes it names. It names the probe target the way the WireGuard
// adapter's AllowedIPs do.
type carried struct {
	whole  bool
	routes []netip.Prefix
	around []netip.Prefix // routes through net_gateway, which go around it
	dns    []netip.Addr
}

// push_marker starts the log line openvpn.exe writes at --verb 3 for each
// control message from the node; a long route list comes as several.
const push_marker = "PUSH_REPLY,"

// read_profile takes the options of the profile itself into account.
func (c *carried) read_profile(data []byte) {
	for _, line := range strings.Split(string(data), "\n") {
		c.option(tokens(strings.TrimSpace(line)))
	}
}

// read_push takes a log line into account if it is a PUSH_REPLY.
func (c *carried) read_push(line string) {
	at := strings.Index(line, push_marker)
	if at < 0 {
		return
	}
	message := strings.TrimSuffix(strings.TrimSpace(line[at+len(push_marker):]), "'")
	for _, item := range strings.Split(message, ",") {
		c.option(strings.Fields(item))
	}
}

func (c *carried) option(words []string) {
	if len(words) == 0 {
		return
	}
	switch strings.ToLower(words[0]) {
	case "redirect-gateway":
		c.whole = true
	case "route":
		if len(words) < 2 {
			return
		}
		mask := "255.255.255.255"
		if len(words) >= 3 {
			mask = words[2]
		}
		prefix, ok := ipv4_prefix(words[1], mask)
		switch {
		case !ok:
		case len(words) >= 4 && strings.EqualFold(words[3], "net_gateway"):
			c.around = append(c.around, prefix.Masked())
		default:
			c.routes = append(c.routes, prefix.Masked())
		}
	case "ifconfig":
		// With topology subnet the second word is the mask, and the
		// tunnel's own subnet is carried; with net30 it is the peer's
		// address, which a route names if anything is to reach it.
		if len(words) >= 3 {
			if prefix, ok := ipv4_prefix(words[1], words[2]); ok {
				c.routes = append(c.routes, prefix.Masked())
			}
		}
	case "dhcp-option":
		if len(words) >= 3 && strings.EqualFold(words[1], "DNS") {
			if address, err := netip.ParseAddr(words[2]); err == nil && address.Is4() {
				c.dns = append(c.dns, address)
			}
		}
	}
}

// ipv4_prefix reads an address and a dotted mask; a mask that is not one
// (the net30 peer address) is not a prefix.
func ipv4_prefix(address, mask string) (netip.Prefix, bool) {
	a, err := netip.ParseAddr(address)
	if err != nil || !a.Is4() {
		return netip.Prefix{}, false
	}
	m := net.ParseIP(mask).To4()
	if m == nil {
		return netip.Prefix{}, false
	}
	ones, bits := net.IPMask(m).Size()
	if bits == 0 {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, ones), true
}

// probe_target is the node's resolver on TCP port 53 when the tunnel
// carries only part of the traffic and that resolver is inside it; empty
// when it carries everything (the static target goes through it) or when
// nothing it carries is known to answer.
func (c carried) probe_target() string {
	if c.whole {
		return ""
	}
	for _, address := range c.dns {
		if c.through(address) {
			return net.JoinHostPort(address.String(), "53")
		}
	}
	return ""
}

// through says whether the most specific route to address is the tunnel's,
// as the routing table decides.
func (c carried) through(address netip.Addr) bool {
	best, tunnel := -1, false
	if c.whole {
		best, tunnel = 0, true
	}
	for _, prefix := range c.routes {
		if prefix.Contains(address) && prefix.Bits() > best {
			best, tunnel = prefix.Bits(), true
		}
	}
	for _, prefix := range c.around {
		if prefix.Contains(address) && prefix.Bits() >= best {
			best, tunnel = prefix.Bits(), false
		}
	}
	return tunnel
}

// Probe_target is what a health probe should dial through this tunnel when
// the static target would go around it: see carried.probe_target. Empty
// while no tunnel is up.
func (a *Adapter) Probe_target() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		return ""
	}
	return a.progress.carried.probe_target()
}

// Full_probe_routes confirms both public health targets use this full tunnel.
func (a *Adapter) Full_probe_routes() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.progress.carried
	return a.session != nil && c.whole && c.through(netip.MustParseAddr("1.1.1.1")) && c.through(netip.MustParseAddr("8.8.8.8"))
}
