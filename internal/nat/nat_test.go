package nat

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
)

var (
	steady4   = netip.MustParseAddr("100.88.0.2")
	assigned4 = netip.MustParseAddr("10.29.8.7")
	far4      = netip.MustParseAddr("198.51.100.10")
	router4   = netip.MustParseAddr("203.0.113.1")
	steady6   = netip.MustParseAddr("fd8c:4f2e:a1d7::2")
	assigned6 = netip.MustParseAddr("fd00:29:8::7")
	far6      = netip.MustParseAddr("2001:db8::10")
	router6   = netip.MustParseAddr("2001:db8:ffff::1")
)

func both(t *testing.T) Translation {
	t.Helper()
	translation, err := New(Pair{Steady: steady4, Assigned: assigned4}, Pair{Steady: steady6, Assigned: assigned6})
	if err != nil {
		t.Fatal(err)
	}
	return translation
}

// --- packets ---------------------------------------------------------

// sum is the ones'-complement sum of the parts laid end to end, every part
// but the last of even length. It is written out again here rather than
// taken from the package, so the tests check its arithmetic instead of
// sharing it. A checksum holds when the sum over everything it covers,
// itself included, is all ones.
func sum(parts ...[]byte) uint16 {
	var s uint32
	for _, part := range parts {
		for i := 0; i+1 < len(part); i += 2 {
			s += uint32(part[i])<<8 | uint32(part[i+1])
		}
		if len(part)%2 == 1 {
			s += uint32(part[len(part)-1]) << 8
		}
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return uint16(s)
}

func seal(field []byte, udp bool, covered ...[]byte) {
	field[0], field[1] = 0, 0
	value := ^sum(covered...)
	if udp && value == 0 {
		value = 0xffff
	}
	binary.BigEndian.PutUint16(field, value)
}

func pseudo4(header []byte, length int) []byte {
	pseudo := make([]byte, 12)
	copy(pseudo, header[12:20])
	pseudo[9] = header[9]
	binary.BigEndian.PutUint16(pseudo[10:], uint16(length))
	return pseudo
}

func pseudo6(header []byte, protocol byte, length int) []byte {
	pseudo := make([]byte, 40)
	copy(pseudo, header[8:40])
	binary.BigEndian.PutUint32(pseudo[32:], uint32(length))
	pseudo[39] = protocol
	return pseudo
}

func tcp(source, destination uint16, data string) []byte {
	segment := make([]byte, 20+len(data))
	binary.BigEndian.PutUint16(segment[0:], source)
	binary.BigEndian.PutUint16(segment[2:], destination)
	binary.BigEndian.PutUint32(segment[4:], 0x01020304)
	segment[12] = 5 << 4
	segment[13] = 0x18
	binary.BigEndian.PutUint16(segment[14:], 64240)
	copy(segment[20:], data)
	return segment
}

func udp(source, destination uint16, data string) []byte {
	datagram := make([]byte, 8+len(data))
	binary.BigEndian.PutUint16(datagram[0:], source)
	binary.BigEndian.PutUint16(datagram[2:], destination)
	binary.BigEndian.PutUint16(datagram[4:], uint16(len(datagram)))
	copy(datagram[8:], data)
	return datagram
}

func echo(kind byte, data string) []byte {
	return append([]byte{kind, 0, 0, 0, 0x12, 0x34, 0, 1}, data...)
}

// ipv4 builds a packet whose checksums all hold.
func ipv4(protocol byte, source, destination netip.Addr, payload []byte) []byte {
	packet := make([]byte, 20+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:], uint16(len(packet)))
	binary.BigEndian.PutUint16(packet[4:], 0x5a5a)
	packet[8] = 64
	packet[9] = protocol
	s, d := source.As4(), destination.As4()
	copy(packet[12:], s[:])
	copy(packet[16:], d[:])
	copy(packet[20:], payload)
	l4 := packet[20:]
	switch protocol {
	case 6:
		seal(l4[16:18], false, pseudo4(packet, len(l4)), l4)
	case 17:
		seal(l4[6:8], true, pseudo4(packet, len(l4)), l4)
	case 1:
		seal(l4[2:4], false, l4)
	}
	seal(packet[10:12], false, packet[:20])
	return packet
}

// ipv6 builds a packet whose checksums all hold.
func ipv6(protocol byte, source, destination netip.Addr, payload []byte) []byte {
	packet := make([]byte, 40+len(payload))
	packet[0] = 0x60
	binary.BigEndian.PutUint16(packet[4:], uint16(len(payload)))
	packet[6] = protocol
	packet[7] = 64
	s, d := source.As16(), destination.As16()
	copy(packet[8:], s[:])
	copy(packet[24:], d[:])
	copy(packet[40:], payload)
	l4 := packet[40:]
	switch protocol {
	case 6:
		seal(l4[16:18], false, pseudo6(packet, protocol, len(l4)), l4)
	case 17:
		seal(l4[6:8], true, pseudo6(packet, protocol, len(l4)), l4)
	case 58:
		seal(l4[2:4], false, pseudo6(packet, protocol, len(l4)), l4)
	}
	return packet
}

// with_hop_by_hop puts an options header, padding only, between an IPv6
// packet's header and its payload. The transport checksum does not cover
// it, so it still holds.
func with_hop_by_hop(packet []byte) []byte {
	options := []byte{packet[6], 0, 1, 4, 0, 0, 0, 0}
	out := append(append(append([]byte{}, packet[:40]...), options...), packet[40:]...)
	out[6] = 0
	binary.BigEndian.PutUint16(out[4:], uint16(len(out)-40))
	return out
}

func source4(packet []byte) netip.Addr      { return netip.AddrFrom4([4]byte(packet[12:16])) }
func destination4(packet []byte) netip.Addr { return netip.AddrFrom4([4]byte(packet[16:20])) }
func source6(packet []byte) netip.Addr      { return netip.AddrFrom16([16]byte(packet[8:24])) }
func destination6(packet []byte) netip.Addr { return netip.AddrFrom16([16]byte(packet[24:40])) }

// holds4 checks every checksum an IPv4 packet carries: its header's, and
// its transport's unless it is a later fragment. A quoted packet is cut
// short, so only its header is checked by default.
func holds4(t *testing.T, what string, packet []byte, transport bool) {
	t.Helper()
	header_length := int(packet[0]&0x0f) * 4
	if sum(packet[:header_length]) != 0xffff {
		t.Errorf("%s: the IPv4 header checksum does not hold", what)
	}
	if !transport {
		return
	}
	l4 := packet[header_length:]
	switch packet[9] {
	case 6:
		if sum(pseudo4(packet, len(l4)), l4) != 0xffff {
			t.Errorf("%s: the TCP checksum does not hold", what)
		}
	case 17:
		if binary.BigEndian.Uint16(l4[6:8]) != 0 && sum(pseudo4(packet, len(l4)), l4) != 0xffff {
			t.Errorf("%s: the UDP checksum does not hold", what)
		}
	case 1:
		if sum(l4) != 0xffff {
			t.Errorf("%s: the ICMP checksum does not hold", what)
		}
	}
}

// holds6 checks the transport checksum of an IPv6 packet whose payload is
// that transport, from offset at.
func holds6(t *testing.T, what string, packet []byte, protocol byte, at int) {
	t.Helper()
	l4 := packet[at:]
	if sum(pseudo6(packet, protocol, len(l4)), l4) != 0xffff {
		t.Errorf("%s: the checksum (protocol %d) does not hold", what, protocol)
	}
}

// --- IPv4 --------------------------------------------------------------

func Test_a_tcp_packet_leaving_takes_the_assigned_address_and_its_checksums_hold(t *testing.T) {
	packet := ipv4(6, steady4, far4, tcp(51000, 443, "GET / HTTP/1.1\r\n"))
	if !both(t).Outbound(packet) {
		t.Fatal("Outbound() = false for a packet from the steady address")
	}
	if source4(packet) != assigned4 || destination4(packet) != far4 {
		t.Fatalf("addresses after = %s -> %s; want %s -> %s", source4(packet), destination4(packet), assigned4, far4)
	}
	holds4(t, "leaving", packet, true)
}

func Test_a_udp_packet_arriving_takes_the_steady_address_and_its_checksums_hold(t *testing.T) {
	packet := ipv4(17, far4, assigned4, udp(53, 51000, "an answer, odd length"))
	if !both(t).Inbound(packet) {
		t.Fatal("Inbound() = false for a packet to the assigned address")
	}
	if source4(packet) != far4 || destination4(packet) != steady4 {
		t.Fatalf("addresses after = %s -> %s; want %s -> %s", source4(packet), destination4(packet), far4, steady4)
	}
	holds4(t, "arriving", packet, true)
}

func Test_a_udp_packet_without_a_checksum_keeps_none(t *testing.T) {
	packet := ipv4(17, steady4, far4, udp(51000, 53, "query"))
	packet[26], packet[27] = 0, 0
	both(t).Outbound(packet)
	if packet[26] != 0 || packet[27] != 0 {
		t.Fatalf("UDP checksum after = %#04x; want 0, none", binary.BigEndian.Uint16(packet[26:28]))
	}
	holds4(t, "leaving", packet, true)
}

// A UDP checksum of zero means none; one that comes out zero is sent as
// all ones.
func Test_a_udp_checksum_that_comes_out_zero_is_sent_as_all_ones(t *testing.T) {
	for word := range 0x10000 {
		data := string([]byte{byte(word >> 8), byte(word)})
		after := ipv4(17, assigned4, far4, udp(51000, 53, data))
		after[26], after[27] = 0, 0
		if ^sum(pseudo4(after, 10), after[20:]) != 0 {
			continue
		}
		packet := ipv4(17, steady4, far4, udp(51000, 53, data))
		both(t).Outbound(packet)
		if got := binary.BigEndian.Uint16(packet[26:28]); got != 0xffff {
			t.Fatalf("UDP checksum after = %#04x; want 0xffff", got)
		}
		holds4(t, "leaving", packet, true)
		return
	}
	t.Fatal("no datagram whose checksum comes out zero was found")
}

func Test_an_icmp_echo_changes_only_its_address(t *testing.T) {
	packet := ipv4(1, steady4, far4, echo(8, "ping"))
	icmp_sum := binary.BigEndian.Uint16(packet[22:24])
	both(t).Outbound(packet)
	if source4(packet) != assigned4 || binary.BigEndian.Uint16(packet[22:24]) != icmp_sum {
		t.Fatalf("source %s, ICMP checksum %#04x -> %#04x; want %s and the checksum as it was",
			source4(packet), icmp_sum, binary.BigEndian.Uint16(packet[22:24]), assigned4)
	}
	holds4(t, "leaving", packet, true)
}

// A later fragment carries no transport header: what looks like one is
// data, and stays as it was.
func Test_a_later_fragment_changes_only_its_header(t *testing.T) {
	packet := ipv4(17, steady4, far4, udp(51000, 53, "the middle of a datagram"))
	binary.BigEndian.PutUint16(packet[6:8], 185)
	seal(packet[10:12], false, packet[:20])
	payload := append([]byte{}, packet[20:]...)
	both(t).Outbound(packet)
	if source4(packet) != assigned4 || !bytes.Equal(packet[20:], payload) {
		t.Fatalf("source %s, payload changed %v; want %s and the payload untouched", source4(packet), !bytes.Equal(packet[20:], payload), assigned4)
	}
	holds4(t, "leaving", packet, false)
}

// A router's "fragmentation needed" quotes the packet that left: its source
// is the machine's, and the machine must find its own socket in it.
func Test_an_icmp_error_arriving_quotes_the_packet_with_the_steady_address(t *testing.T) {
	left := ipv4(17, assigned4, far4, udp(51000, 53, "query"))
	packet := ipv4(1, router4, assigned4, append([]byte{3, 4, 0, 0, 0, 0, 0x05, 0x78}, left...))
	both(t).Inbound(packet)
	quoted := packet[28:]
	if destination4(packet) != steady4 || source4(quoted) != steady4 || destination4(quoted) != far4 {
		t.Fatalf("outer destination %s, quoted %s -> %s; want %s, and %s -> %s",
			destination4(packet), source4(quoted), destination4(quoted), steady4, steady4, far4)
	}
	holds4(t, "the error", packet, true)
	holds4(t, "the packet it quotes", quoted, true)
}

// The machine's "port unreachable" quotes the packet that arrived, which
// came to the steady address; the node must see its own address there.
func Test_an_icmp_error_leaving_quotes_the_packet_with_the_assigned_address(t *testing.T) {
	arrived := ipv4(17, far4, steady4, udp(53, 51000, "late answer"))
	packet := ipv4(1, steady4, far4, append([]byte{3, 3, 0, 0, 0, 0, 0, 0}, arrived...))
	both(t).Outbound(packet)
	quoted := packet[28:]
	if source4(packet) != assigned4 || source4(quoted) != far4 || destination4(quoted) != assigned4 {
		t.Fatalf("outer source %s, quoted %s -> %s; want %s, and %s -> %s",
			source4(packet), source4(quoted), destination4(quoted), assigned4, far4, assigned4)
	}
	holds4(t, "the error", packet, true)
	holds4(t, "the packet it quotes", quoted, true)
}

// --- IPv6 --------------------------------------------------------------

func Test_an_ipv6_tcp_packet_leaving_takes_the_assigned_address_and_its_checksum_holds(t *testing.T) {
	packet := ipv6(6, steady6, far6, tcp(51000, 443, "hello"))
	if !both(t).Outbound(packet) {
		t.Fatal("Outbound() = false for a packet from the steady address")
	}
	if source6(packet) != assigned6 || destination6(packet) != far6 {
		t.Fatalf("addresses after = %s -> %s; want %s -> %s", source6(packet), destination6(packet), assigned6, far6)
	}
	holds6(t, "leaving", packet, 6, 40)
}

func Test_an_icmpv6_echo_arriving_takes_the_steady_address_and_its_checksum_holds(t *testing.T) {
	packet := ipv6(58, far6, assigned6, echo(129, "pong"))
	both(t).Inbound(packet)
	if destination6(packet) != steady6 {
		t.Fatalf("destination after = %s; want %s", destination6(packet), steady6)
	}
	holds6(t, "arriving", packet, 58, 40)
}

func Test_ipv6_extension_headers_are_walked_to_the_transport_checksum(t *testing.T) {
	packet := with_hop_by_hop(ipv6(17, far6, assigned6, udp(53, 51000, "answer")))
	both(t).Inbound(packet)
	if destination6(packet) != steady6 {
		t.Fatalf("destination after = %s; want %s", destination6(packet), steady6)
	}
	holds6(t, "arriving", packet, 17, 48)
}

func Test_an_ipv6_later_fragment_changes_only_its_address(t *testing.T) {
	packet := ipv6(17, steady6, far6, udp(51000, 53, "the middle of a datagram"))
	fragment := []byte{17, 0, 0x05, 0x40, 0, 0, 0, 7}
	packet = append(append(append([]byte{}, packet[:40]...), fragment...), packet[40:]...)
	packet[6] = 44
	binary.BigEndian.PutUint16(packet[4:], uint16(len(packet)-40))
	payload := append([]byte{}, packet[40:]...)
	both(t).Outbound(packet)
	if source6(packet) != assigned6 || !bytes.Equal(packet[40:], payload) {
		t.Fatalf("source %s, payload changed %v; want %s and the payload untouched", source6(packet), !bytes.Equal(packet[40:], payload), assigned6)
	}
}

func Test_an_icmpv6_packet_too_big_arriving_quotes_the_packet_with_the_steady_address(t *testing.T) {
	left := ipv6(17, assigned6, far6, udp(51000, 443, "a large datagram"))
	packet := ipv6(58, router6, assigned6, append([]byte{2, 0, 0, 0, 0, 0, 0x05, 0x00}, left...))
	both(t).Inbound(packet)
	quoted := packet[48:]
	if destination6(packet) != steady6 || source6(quoted) != steady6 {
		t.Fatalf("outer destination %s, quoted source %s; want %s both", destination6(packet), source6(quoted), steady6)
	}
	holds6(t, "the error", packet, 58, 40)
	holds6(t, "the packet it quotes", quoted, 17, 40)
}

// --- what is not the tunnel's -------------------------------------------

func Test_packets_not_of_the_tunnel_are_left_as_they_are(t *testing.T) {
	only4, err := New(Pair{Steady: steady4, Assigned: assigned4})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		translation Translation
		packet      []byte
		outbound    bool
	}{
		{"leaving from another address", both(t), ipv4(6, far4, router4, tcp(1, 2, "x")), true},
		{"arriving for the steady address", both(t), ipv4(6, far4, steady4, tcp(1, 2, "x")), false},
		{"leaving with the steady address only as its destination", both(t), ipv4(17, far4, steady4, udp(1, 2, "x")), true},
		{"ipv6 with no ipv6 pair", only4, ipv6(6, steady6, far6, tcp(1, 2, "x")), true},
		{"nothing to translate", Translation{}, ipv4(6, steady4, far4, tcp(1, 2, "x")), true},
		{"empty", both(t), []byte{}, true},
		{"not ip", both(t), []byte{0x50, 1, 2, 3}, true},
		{"ipv4 longer than it is", both(t), ipv4(6, steady4, far4, tcp(1, 2, "x"))[:30], true},
		{"ipv6 longer than it is", both(t), ipv6(6, steady6, far6, tcp(1, 2, "x"))[:50], true},
		{"ipv4 header too short", both(t), []byte{0x44, 0, 0, 20, 0, 0, 0, 0, 64, 6, 0, 0, 100, 88, 0, 2, 1, 2, 3, 4}, true},
	}
	for _, c := range cases {
		before := append([]byte{}, c.packet...)
		var changed bool
		if c.outbound {
			changed = c.translation.Outbound(c.packet)
		} else {
			changed = c.translation.Inbound(c.packet)
		}
		if changed || !bytes.Equal(c.packet, before) {
			t.Errorf("%s: changed = %v, packet changed = %v; want neither", c.name, changed, !bytes.Equal(c.packet, before))
		}
	}
}

func Test_new_takes_one_pair_of_one_family_each(t *testing.T) {
	if _, err := New(Pair{Steady: steady4, Assigned: assigned6}); err == nil {
		t.Error("New() accepted a pair of two families")
	}
	if _, err := New(Pair{Steady: steady4}); err == nil {
		t.Error("New() accepted a pair with no assigned address")
	}
	if _, err := New(Pair{Steady: steady4, Assigned: assigned4}, Pair{Steady: far4, Assigned: router4}); err == nil {
		t.Error("New() accepted two pairs of one family")
	}
	if _, err := New(Pair{Steady: netip.AddrFrom16(steady4.As16()), Assigned: assigned4}); err != nil {
		t.Errorf("New() refused an IPv4 address written as IPv6: %v", err)
	}
}

// A packet is whatever the adapter hands over; nothing in it may make the
// translation read or write past its end.
func Fuzz_translation_stays_inside_the_packet(f *testing.F) {
	left4 := ipv4(17, assigned4, far4, udp(51000, 53, "query"))
	left6 := ipv6(17, assigned6, far6, udp(51000, 443, "data"))
	for _, seed := range [][]byte{
		ipv4(6, steady4, far4, tcp(51000, 443, "x")),
		ipv4(17, far4, assigned4, udp(53, 51000, "y")),
		ipv4(1, router4, assigned4, append([]byte{3, 4, 0, 0, 0, 0, 5, 0x78}, left4...)),
		ipv6(6, steady6, far6, tcp(51000, 443, "x")),
		with_hop_by_hop(ipv6(17, far6, assigned6, udp(53, 51000, "y"))),
		ipv6(58, router6, assigned6, append([]byte{2, 0, 0, 0, 0, 0, 5, 0}, left6...)),
	} {
		f.Add(seed)
	}
	translation, err := New(Pair{Steady: steady4, Assigned: assigned4}, Pair{Steady: steady6, Assigned: assigned6})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, packet []byte) {
		length := len(packet)
		translation.Outbound(packet)
		translation.Inbound(packet)
		if len(packet) != length {
			t.Fatalf("length %d -> %d", length, len(packet))
		}
	})
}
