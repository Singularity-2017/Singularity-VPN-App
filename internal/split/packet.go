// Package split is the per-process path of stage 9 in pure code: it
// rewrites a game's IPv4 packets between the physical address they left
// with and the tunnel address wireguard-go carries, and remembers which
// local ports it did that for so the replies can go back the same way.
//
// Nothing here touches a driver or a socket. The packet work is done on
// byte slices so every checksum can be checked by a test.
package split

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// IANA protocol numbers this package rewrites.
const (
	Protocol_tcp = 6
	Protocol_udp = 17
)

// ipv4 is what this package needs from a packet's IPv4 header.
type ipv4 struct {
	header_length  int
	protocol       uint8
	first_fragment bool // the only fragment that carries the transport header
}

func parse_ipv4(packet []byte) (ipv4, error) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ipv4{}, fmt.Errorf("split: not an IPv4 packet")
	}
	header_length := int(packet[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(packet[2:4]))
	if header_length < 20 || total < header_length || total > len(packet) {
		return ipv4{}, fmt.Errorf("split: malformed IPv4 header")
	}
	fragment_offset := binary.BigEndian.Uint16(packet[6:8]) & 0x1fff
	return ipv4{header_length: header_length, protocol: packet[9], first_fragment: fragment_offset == 0}, nil
}

// transport returns the TCP or UDP header and the offset of its checksum,
// or nil when the packet carries no such header this package can rewrite.
func (h ipv4) transport(packet []byte) (header []byte, checksum_at int) {
	if !h.first_fragment {
		return nil, 0
	}
	l4 := packet[h.header_length:binary.BigEndian.Uint16(packet[2:4])]
	switch {
	case h.protocol == Protocol_tcp && len(l4) >= 20:
		return l4, 16
	case h.protocol == Protocol_udp && len(l4) >= 8:
		return l4, 6
	}
	return nil, 0
}

// Flow is a packet's protocol and its two ends.
type Flow struct {
	Protocol    uint8
	Source      netip.AddrPort
	Destination netip.AddrPort
}

// Flow_of reads a TCP or UDP IPv4 packet's flow. A later fragment, which
// carries no ports, is not a flow this package can route.
func Flow_of(packet []byte) (Flow, bool) {
	h, err := parse_ipv4(packet)
	if err != nil {
		return Flow{}, false
	}
	l4, _ := h.transport(packet)
	if l4 == nil {
		return Flow{}, false
	}
	source := netip.AddrFrom4([4]byte(packet[12:16]))
	destination := netip.AddrFrom4([4]byte(packet[16:20]))
	return Flow{
		Protocol:    h.protocol,
		Source:      netip.AddrPortFrom(source, binary.BigEndian.Uint16(l4[0:2])),
		Destination: netip.AddrPortFrom(destination, binary.BigEndian.Uint16(l4[2:4])),
	}, true
}

const (
	source_at      = 12
	destination_at = 16
)

// rewrite_address replaces the source or destination address in place and
// updates both checksums incrementally (RFC 1624): the IPv4 header's, and
// the transport header's, which covers the address through the
// pseudo-header. A UDP checksum of zero means "none" and stays zero.
func rewrite_address(packet []byte, at int, to netip.Addr) error {
	if !to.Is4() {
		return fmt.Errorf("split: %s is not an IPv4 address", to)
	}
	h, err := parse_ipv4(packet)
	if err != nil {
		return err
	}
	replacement := to.As4()
	old := packet[at : at+4]
	if l4, checksum_at := h.transport(packet); l4 != nil {
		field := l4[checksum_at : checksum_at+2]
		sum := binary.BigEndian.Uint16(field)
		if !(h.protocol == Protocol_udp && sum == 0) {
			sum = update_checksum(sum, old, replacement[:])
			if h.protocol == Protocol_udp && sum == 0 {
				sum = 0xffff
			}
			binary.BigEndian.PutUint16(field, sum)
		}
	}
	header_sum := update_checksum(binary.BigEndian.Uint16(packet[10:12]), old, replacement[:])
	binary.BigEndian.PutUint16(packet[10:12], header_sum)
	copy(old, replacement[:])
	return nil
}

// recompute_checksums computes both checksums from scratch. A packet
// captured on its way out may carry only the partial sum Windows leaves
// for the network card to finish (checksum offload), which an incremental
// update would carry on being wrong. A first fragment of a larger datagram
// cannot have its transport sum recomputed without the rest, and keeps the
// incremental one.
func recompute_checksums(packet []byte) {
	h, err := parse_ipv4(packet)
	if err != nil {
		return
	}
	binary.BigEndian.PutUint16(packet[10:12], 0)
	binary.BigEndian.PutUint16(packet[10:12], sum_of(packet[:h.header_length]))
	more_fragments := packet[6]&0x20 != 0
	l4, checksum_at := h.transport(packet)
	if l4 == nil || more_fragments {
		return
	}
	var pseudo [12]byte
	copy(pseudo[0:8], packet[12:20])
	pseudo[9] = h.protocol
	binary.BigEndian.PutUint16(pseudo[10:], uint16(len(l4)))
	binary.BigEndian.PutUint16(l4[checksum_at:], 0)
	sum := sum_of(pseudo[:], l4)
	if h.protocol == Protocol_udp && sum == 0 {
		sum = 0xffff
	}
	binary.BigEndian.PutUint16(l4[checksum_at:], sum)
}

// sum_of is the ones'-complement checksum of the parts laid end to end;
// every part but the last has an even length.
func sum_of(parts ...[]byte) uint16 {
	var s uint32
	for _, part := range parts {
		for i := 0; i+1 < len(part); i += 2 {
			s += uint32(binary.BigEndian.Uint16(part[i:]))
		}
		if len(part)%2 == 1 {
			s += uint32(part[len(part)-1]) << 8
		}
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

// update_checksum adjusts a ones'-complement checksum for 16-bit words
// changing from old to replacement: HC' = ~(~HC + ~m + m').
func update_checksum(sum uint16, old, replacement []byte) uint16 {
	s := uint32(^sum)
	for i := 0; i+1 < len(old); i += 2 {
		s += uint32(^binary.BigEndian.Uint16(old[i:]))
		s += uint32(binary.BigEndian.Uint16(replacement[i:]))
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

// clamp_mss lowers the MSS option of a TCP SYN to at most mss, so neither
// end sends segments that would not fit the tunnel. Anything else is left
// as it is.
func clamp_mss(packet []byte, mss uint16) {
	h, err := parse_ipv4(packet)
	if err != nil || h.protocol != Protocol_tcp {
		return
	}
	l4, checksum_at := h.transport(packet)
	if l4 == nil || l4[13]&0x02 == 0 { // SYN
		return
	}
	data_offset := int(l4[12]>>4) * 4
	if data_offset < 20 || data_offset > len(l4) {
		return
	}
	options := l4[20:data_offset]
	for i := 0; i < len(options); {
		kind := options[i]
		switch {
		case kind == 0:
			return
		case kind == 1:
			i++
			continue
		case i+1 >= len(options) || options[i+1] < 2 || i+int(options[i+1]) > len(options):
			return
		case kind == 2 && options[i+1] == 4:
			value := options[i+2 : i+4]
			if binary.BigEndian.Uint16(value) > mss {
				var clamped [2]byte
				binary.BigEndian.PutUint16(clamped[:], mss)
				field := l4[checksum_at : checksum_at+2]
				binary.BigEndian.PutUint16(field, update_checksum(binary.BigEndian.Uint16(field), value, clamped[:]))
				copy(value, clamped[:])
			}
			return
		}
		i += int(options[i+1])
	}
}
