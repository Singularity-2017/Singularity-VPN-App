// Package nat translates one address of a packet into another, one to one,
// keeping every checksum that covers it right. It lets the tunnel adapter
// carry one address for the life of its session while each node the tunnel
// goes to knows the machine by the address it gave it.
//
// The address a node gives is the node's own: AntiZapret numbers its
// clients per node, per protocol and per mode. Put on the adapter, it
// changed at nearly every server switch, and on Windows 11 26H2 an address
// change sets every Chromium-based program on the machine asking the DNS
// Client service for its adapters for a minute or more, the service
// spinning most cores while it answers them one at a time -- measured on
// the owner's machine 2026-10-30: one address added and removed cost 1,105
// core-seconds over 76 seconds, forty routes nothing. So the adapter's
// address stays, and the packets change instead.
//
// Checksums are updated, not computed afresh, so they must come in complete:
// Wintun hands over packets whose checksums the stack has finished. A packet
// captured on its way to a network card may carry only the partial sum that
// checksum offload leaves for the card (see package split), and is not one
// to translate here.
//
// Nothing here touches a device or a socket: the work is done on byte
// slices, so every checksum can be checked by a test.
package nat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
)

// IANA protocol numbers whose checksums this package keeps right.
const (
	protocol_icmp   = 1
	protocol_tcp    = 6
	protocol_udp    = 17
	protocol_icmpv6 = 58
)

// Pair is one family's translation: the address the adapter carries, and
// the one the node gave the machine, which packets carry in its place.
type Pair struct {
	Steady   netip.Addr
	Assigned netip.Addr
}

// Translation holds at most one Pair for each family. Its zero value
// translates nothing.
type Translation struct {
	ipv4, ipv6 family
}

// family is one Pair as the bytes a header carries; nil when the family is
// not translated.
type family struct {
	steady, assigned []byte
}

// New builds a Translation from pairs, at most one of each family.
func New(pairs ...Pair) (Translation, error) {
	var t Translation
	for _, pair := range pairs {
		steady, assigned := pair.Steady.Unmap(), pair.Assigned.Unmap()
		if !steady.IsValid() || !assigned.IsValid() || steady.Is4() != assigned.Is4() {
			return Translation{}, fmt.Errorf("nat: %s and %s are not two addresses of one family", pair.Steady, pair.Assigned)
		}
		slot := &t.ipv6
		if steady.Is4() {
			slot = &t.ipv4
		}
		if slot.steady != nil {
			return Translation{}, fmt.Errorf("nat: a second pair for the family of %s", steady)
		}
		*slot = family{steady: steady.AsSlice(), assigned: assigned.AsSlice()}
	}
	return t, nil
}

// direction is the way a packet goes.
type direction int

const (
	// outbound: a packet leaving the machine from the steady address
	// leaves from the assigned one.
	outbound direction = iota
	// inbound: a packet arriving for the assigned address is delivered to
	// the steady one.
	inbound
)

// Outbound translates a packet leaving the machine: one from the steady
// address of its family leaves from the assigned one. It reports whether
// the packet changed; any other is left as it is.
func (t Translation) Outbound(packet []byte) bool {
	return t.translate(packet, outbound)
}

// Inbound translates a packet arriving: one for the assigned address of
// its family is delivered to the steady one. It reports whether the packet
// changed; any other is left as it is.
func (t Translation) Inbound(packet []byte) bool {
	return t.translate(packet, inbound)
}

func (t Translation) translate(packet []byte, way direction) bool {
	if len(packet) == 0 {
		return false
	}
	switch packet[0] >> 4 {
	case 4:
		if t.ipv4.steady != nil {
			from, to := t.ipv4.sides(way)
			return translate_ipv4(packet, way, from, to)
		}
	case 6:
		if t.ipv6.steady != nil {
			from, to := t.ipv6.sides(way)
			return translate_ipv6(packet, way, from, to)
		}
	}
	return false
}

// sides is what a packet going way carries, and what it carries instead.
func (f family) sides(way direction) (from, to []byte) {
	if way == outbound {
		return f.steady, f.assigned
	}
	return f.assigned, f.steady
}

// at is where the address translated sits in an IPv4 or IPv6 header: the
// machine's own end of the packet -- its source leaving, its destination
// arriving -- or, in the packet an ICMP error quotes, which went the other
// way, the other end.
func at(way direction, ipv6, quoted bool) int {
	source := (way == outbound) != quoted
	switch {
	case ipv6 && source:
		return 8
	case ipv6:
		return 24
	case source:
		return 12
	}
	return 16
}

func translate_ipv4(packet []byte, way direction, from, to []byte) bool {
	if len(packet) < 20 {
		return false
	}
	header_length := int(packet[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(packet[2:4]))
	if header_length < 20 || total < header_length || total > len(packet) {
		return false
	}
	address := packet[at(way, false, false):][:4]
	if !bytes.Equal(address, from) {
		return false
	}
	copy(address, to)
	adjust(packet[10:12], from, to)
	if binary.BigEndian.Uint16(packet[6:8])&0x1fff != 0 {
		// A later fragment: the transport header, and its checksum, are in
		// the first.
		return true
	}
	payload := packet[header_length:total]
	whole := packet[6]&0x20 == 0
	if packet[9] == protocol_icmp && whole && is_icmp_error(payload) && translate_quoted_ipv4(payload[8:], way, from, to) {
		reseal(payload[2:4], payload)
		return true
	}
	adjust_transport(packet[9], payload, from, to, false)
	return true
}

// translate_quoted_ipv4 translates the packet an ICMP error quotes, the one
// the error is about. It reports whether that was a packet of the tunnel.
// A quoted packet is cut short: its length field is not its length here,
// and a transport header may stop before its checksum.
func translate_quoted_ipv4(quoted []byte, way direction, from, to []byte) bool {
	if len(quoted) < 20 || quoted[0]>>4 != 4 {
		return false
	}
	header_length := int(quoted[0]&0x0f) * 4
	if header_length < 20 || header_length > len(quoted) {
		return false
	}
	address := quoted[at(way, false, true):][:4]
	if !bytes.Equal(address, from) {
		return false
	}
	copy(address, to)
	adjust(quoted[10:12], from, to)
	if binary.BigEndian.Uint16(quoted[6:8])&0x1fff == 0 {
		adjust_transport(quoted[9], quoted[header_length:], from, to, false)
	}
	return true
}

// is_icmp_error reports whether an ICMP message quotes a packet:
// destination unreachable, source quench, redirect, time exceeded and
// parameter problem.
func is_icmp_error(message []byte) bool {
	if len(message) < 8 {
		return false
	}
	switch message[0] {
	case 3, 4, 5, 11, 12:
		return true
	}
	return false
}

func translate_ipv6(packet []byte, way direction, from, to []byte) bool {
	if len(packet) < 40 {
		return false
	}
	end := 40 + int(binary.BigEndian.Uint16(packet[4:6]))
	if end > len(packet) {
		return false
	}
	address := packet[at(way, true, false):][:16]
	if !bytes.Equal(address, from) {
		return false
	}
	copy(address, to)
	protocol, l4, whole, ok := upper_layer(packet[:end])
	if !ok {
		return true
	}
	if protocol == protocol_icmpv6 && whole && is_icmpv6_error(l4) && translate_quoted_ipv6(l4[8:], way, from, to) {
		var tail [8]byte
		binary.BigEndian.PutUint32(tail[0:4], uint32(len(l4)))
		tail[7] = protocol_icmpv6
		reseal(l4[2:4], packet[8:40], tail[:], l4)
		return true
	}
	adjust_transport(protocol, l4, from, to, true)
	return true
}

// translate_quoted_ipv6 is translate_quoted_ipv4 for an ICMPv6 error.
func translate_quoted_ipv6(quoted []byte, way direction, from, to []byte) bool {
	if len(quoted) < 40 || quoted[0]>>4 != 6 {
		return false
	}
	address := quoted[at(way, true, true):][:16]
	if !bytes.Equal(address, from) {
		return false
	}
	copy(address, to)
	if protocol, l4, _, ok := upper_layer(quoted); ok {
		adjust_transport(protocol, l4, from, to, true)
	}
	return true
}

// is_icmpv6_error reports whether an ICMPv6 message quotes a packet:
// destination unreachable, packet too big, time exceeded and parameter
// problem.
func is_icmpv6_error(message []byte) bool {
	return len(message) >= 8 && message[0] >= 1 && message[0] <= 4
}

// upper_layer walks an IPv6 packet's extension headers to its transport
// header. whole is false for a fragment of a larger packet; ok is false for
// a later fragment, which carries no transport header, and for a header
// this does not walk (AH, ESP), whose payload carries no checksum over the
// address this package could keep right.
func upper_layer(packet []byte) (protocol uint8, l4 []byte, whole, ok bool) {
	next, offset, whole := packet[6], 40, true
	for {
		switch next {
		case 0, 43, 60: // hop-by-hop options, routing, destination options
			if offset+2 > len(packet) {
				return 0, nil, false, false
			}
			next, offset = packet[offset], offset+(int(packet[offset+1])+1)*8
		case 44: // fragment
			if offset+8 > len(packet) || binary.BigEndian.Uint16(packet[offset+2:offset+4])>>3 != 0 {
				return 0, nil, false, false
			}
			whole = whole && packet[offset+3]&1 == 0
			next, offset = packet[offset], offset+8
		case 50, 51, 59: // ESP, AH, no next header
			return 0, nil, false, false
		default:
			if offset > len(packet) {
				return 0, nil, false, false
			}
			return next, packet[offset:], whole, true
		}
	}
}

// adjust_transport keeps a transport checksum that covers the address
// through the pseudo-header right: TCP's, UDP's and, over IPv6, ICMPv6's.
// One cut off before its checksum, as a quoted header may be, is left.
func adjust_transport(protocol uint8, l4 []byte, from, to []byte, ipv6 bool) {
	switch {
	case protocol == protocol_tcp && len(l4) >= 18:
		adjust(l4[16:18], from, to)
	case protocol == protocol_udp && len(l4) >= 8:
		adjust_udp(l4[6:8], from, to)
	case protocol == protocol_icmpv6 && ipv6 && len(l4) >= 4:
		adjust(l4[2:4], from, to)
	}
}

// adjust updates the checksum in field for words changing from old to
// replacement.
func adjust(field, old, replacement []byte) {
	binary.BigEndian.PutUint16(field, update(binary.BigEndian.Uint16(field), old, replacement))
}

// adjust_udp is adjust for a UDP checksum: zero, "none", stays zero, and a
// sum that comes out zero is sent as all ones.
func adjust_udp(field, old, replacement []byte) {
	sum := binary.BigEndian.Uint16(field)
	if sum == 0 {
		return
	}
	if sum = update(sum, old, replacement); sum == 0 {
		sum = 0xffff
	}
	binary.BigEndian.PutUint16(field, sum)
}

// update adjusts a ones'-complement checksum for 16-bit words changing from
// old to replacement (RFC 1624): HC' = ~(~HC + ~m + m').
func update(sum uint16, old, replacement []byte) uint16 {
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

// reseal computes the checksum in field afresh over covered, which holds
// field itself: an ICMP error whose quoted packet changed in several words.
func reseal(field []byte, covered ...[]byte) {
	field[0], field[1] = 0, 0
	binary.BigEndian.PutUint16(field, checksum(covered...))
}

// checksum is the ones'-complement checksum of the parts laid end to end;
// every part but the last has an even length.
func checksum(parts ...[]byte) uint16 {
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
