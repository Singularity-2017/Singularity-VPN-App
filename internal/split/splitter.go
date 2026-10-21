package split

import (
	"net/netip"
	"sync"

	"singularity-vpn-app/internal/matchserver"
)

// Origin is where a game's flow really came from: the address its packets
// left with and the interface the replies must be injected on.
type Origin struct {
	Address         netip.Addr
	Interface_index uint32
	Sub_index       uint32
}

type flow_key struct {
	protocol uint8
	port     uint16
}

// Splitter moves a game's packets between the physical path and the tunnel.
// Outbound packets come from the capture handle; inbound ones from
// wireguard-go. It is safe for concurrent use: the two arrive on different
// goroutines.
type Splitter struct {
	tunnel netip.Addr
	mss    uint16

	mu    sync.Mutex
	flows map[flow_key]Origin
	// sent counts the UDP packets carried to each address and port since
	// the last Take_sent: a match server is where a game sends most.
	sent map[netip.AddrPort]int
}

// New_splitter prepares the path for a tunnel with the given IPv4 address
// and MTU. The MSS is the MTU less the 40 bytes of IPv4 and TCP headers.
func New_splitter(tunnel netip.Addr, mtu int) *Splitter {
	return &Splitter{tunnel: tunnel, mss: uint16(mtu - 40), flows: map[flow_key]Origin{}, sent: map[netip.AddrPort]int{}}
}

// Outbound turns a captured game packet into one wireguard-go can carry:
// its source becomes the tunnel address, and a SYN's MSS is clamped. It
// remembers the original source so the reply can be put back. The packet is
// changed in place; false means it is not one this path carries -- not
// IPv4 TCP or UDP, or a later fragment -- and it should go out as it was.
func (s *Splitter) Outbound(packet []byte, origin Origin) bool {
	flow, ok := Flow_of(packet)
	if !ok {
		return false
	}
	key := flow_key{flow.Protocol, flow.Source.Port()}
	if flow.Protocol == Protocol_tcp && !Is_syn(packet) {
		// A TCP connection is carried only from its first SYN. One that
		// was already open when capture began keeps going direct: moving
		// it mid-stream would change its source address, and the server
		// would reset it.
		s.mu.Lock()
		_, known := s.flows[key]
		s.mu.Unlock()
		if !known {
			return false
		}
	}
	origin.Address = flow.Source.Addr()
	if err := rewrite_address(packet, source_at, s.tunnel); err != nil {
		return false
	}
	clamp_mss(packet, s.mss)
	recompute_checksums(packet)
	s.mu.Lock()
	s.flows[key] = origin
	if to := flow.Destination.Addr(); flow.Protocol == Protocol_udp && to.IsGlobalUnicast() && !to.IsPrivate() {
		// Bounded: a game talking to many peers, or nobody asking,
		// must not grow this without end.
		if _, counted := s.sent[flow.Destination]; counted || len(s.sent) < sent_most {
			s.sent[flow.Destination]++
		}
	}
	s.mu.Unlock()
	return true
}

// sent_most bounds how many destinations Take_sent counts at once.
const sent_most = 1024

// Take_sent gives the UDP packets the games sent each public address and
// port since it was last asked, and starts counting again.
func (s *Splitter) Take_sent() map[netip.AddrPort]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sent := s.sent
	s.sent = map[netip.AddrPort]int{}
	return sent
}

// Take_busiest names the public address the games sent the most UDP
// packets to since it was last asked, by matchserver.Busiest, and starts
// counting again.
func (s *Splitter) Take_busiest(least int) (busiest netip.Addr, most, total int, ok bool) {
	return matchserver.Busiest(s.Take_sent(), least)
}

// Tunnel is the tunnel address the games' packets leave with.
func (s *Splitter) Tunnel() netip.Addr {
	return s.tunnel
}

// Is_syn reports a TCP segment opening a connection: SYN set, ACK not.
func Is_syn(packet []byte) bool {
	h, err := parse_ipv4(packet)
	if err != nil || h.protocol != Protocol_tcp {
		return false
	}
	l4, _ := h.transport(packet)
	return l4 != nil && l4[13]&0x12 == 0x02
}

// Inbound takes a packet wireguard-go has decrypted. A reply to a game flow
// has its destination put back to the original address, its SYN-ACK
// clamped, and is returned with the interface to inject it on. Anything
// else returns false and goes on to the tunnel adapter as before.
func (s *Splitter) Inbound(packet []byte) (Origin, bool) {
	flow, ok := Flow_of(packet)
	if !ok || flow.Destination.Addr() != s.tunnel {
		return Origin{}, false
	}
	s.mu.Lock()
	origin, known := s.flows[flow_key{flow.Protocol, flow.Destination.Port()}]
	s.mu.Unlock()
	if !known {
		return Origin{}, false
	}
	if err := rewrite_address(packet, destination_at, origin.Address); err != nil {
		return Origin{}, false
	}
	clamp_mss(packet, s.mss)
	return origin, true
}

// Retain forgets every flow whose local port keep rejects: the port is no
// longer a game's, and a reply to it belongs to whoever holds it now.
func (s *Splitter) Retain(keep func(protocol uint8, port uint16) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.flows {
		if !keep(key.protocol, key.port) {
			delete(s.flows, key)
		}
	}
}
