package wireguard

import (
	"net/netip"

	"github.com/amnezia-vpn/amneziawg-go/tun"

	"singularity-vpn-app/internal/nat"
)

// The tunnel adapter carries these addresses for the life of its session,
// whichever node the tunnel goes to, and the device puts the address the
// node gave on the packets in their place (package nat). A node numbers its
// clients itself -- per node, per protocol, per mode -- so the address a
// config carries changed at nearly every server switch, and on Windows 11
// 26H2 every change of an adapter's address kept the DNS Client service on
// most cores for a minute or more. A switch now changes no address Windows
// can see, and a disconnect leaves the adapter up with its steady addresses
// and nothing else on it (Adapter.rest).
//
// They come from ranges a home network does not number itself from and
// AntiZapret does not hand out: the shared address space of RFC 6598, which
// carrier-grade NAT uses on the provider's side, and a unique local IPv6
// prefix of their own (RFC 4193). A host somewhere at exactly one of them --
// a Tailscale peer, say -- could not be reached while the adapter is up;
// that is unlikely enough to accept. Neither leaves the machine: a packet
// carries the node's address by the time it is encrypted.
var (
	steady_ipv4 = netip.MustParsePrefix("100.88.0.2/32")
	steady_ipv6 = netip.MustParsePrefix("fd8c:4f2e:a1d7::2/128")
)

// steady_addresses is what the adapter carries for a tunnel the node
// numbered assigned, the adapter carrying carried: a steady address for
// every family either has. A family stays once it is on the adapter, so a
// tunnel without IPv6 after one with it takes no address off.
func steady_addresses(assigned, carried []netip.Prefix) []netip.Prefix {
	var ipv4, ipv6 bool
	for _, list := range [][]netip.Prefix{assigned, carried} {
		for _, prefix := range list {
			if prefix.Addr().Unmap().Is4() {
				ipv4 = true
			} else {
				ipv6 = true
			}
		}
	}
	var steady []netip.Prefix
	if ipv4 {
		steady = append(steady, steady_ipv4)
	}
	if ipv6 {
		steady = append(steady, steady_ipv6)
	}
	return steady
}

// translation_for pairs each steady address with the first address of its
// family the node gave.
func translation_for(assigned []netip.Prefix) (nat.Translation, error) {
	var pairs []nat.Pair
	var ipv4, ipv6 bool
	for _, prefix := range assigned {
		address := prefix.Addr().Unmap()
		switch {
		case address.Is4() && !ipv4:
			ipv4 = true
			pairs = append(pairs, nat.Pair{Steady: steady_ipv4.Addr(), Assigned: address})
		case address.Is6() && !ipv6:
			ipv6 = true
			pairs = append(pairs, nat.Pair{Steady: steady_ipv6.Addr(), Assigned: address})
		}
	}
	return nat.New(pairs...)
}

// translating_tun is the adapter as wireguard-go reads and writes it: what
// the machine sends from a steady address leaves from the node's, and what
// arrives for the node's is delivered to the steady one.
type translating_tun struct {
	tun.Device
	translation nat.Translation
}

func (t *translating_tun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := t.Device.Read(bufs, sizes, offset)
	for i := range n {
		t.translation.Outbound(bufs[i][offset : offset+sizes[i]])
	}
	return n, err
}

func (t *translating_tun) Write(bufs [][]byte, offset int) (int, error) {
	for _, buf := range bufs {
		t.translation.Inbound(buf[offset:])
	}
	return t.Device.Write(bufs, offset)
}
