package split

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// full_checksum is the ones'-complement sum computed from scratch, the
// reference the incremental updates are checked against.
func full_checksum(parts ...[]byte) uint16 {
	var s uint32
	for _, part := range parts {
		for i := 0; i < len(part); i += 2 {
			word := uint32(part[i]) << 8
			if i+1 < len(part) {
				word |= uint32(part[i+1])
			}
			s += word
		}
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

func pseudo_header(packet []byte) []byte {
	l4_length := int(binary.BigEndian.Uint16(packet[2:4])) - 20
	pseudo := make([]byte, 12)
	copy(pseudo[0:8], packet[12:20])
	pseudo[9] = packet[9]
	binary.BigEndian.PutUint16(pseudo[10:], uint16(l4_length))
	return pseudo
}

// build makes an IPv4 packet with correct checksums. l4 is the transport
// header and payload with its checksum field zero.
func build(protocol uint8, source, destination string, l4 []byte) []byte {
	packet := make([]byte, 20+len(l4))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:], uint16(len(packet)))
	packet[8] = 64
	packet[9] = protocol
	s, d := netip.MustParseAddr(source).As4(), netip.MustParseAddr(destination).As4()
	copy(packet[12:], s[:])
	copy(packet[16:], d[:])
	copy(packet[20:], l4)
	binary.BigEndian.PutUint16(packet[10:], full_checksum(packet[:20]))
	at := 16
	if protocol == Protocol_udp {
		at = 6
	}
	binary.BigEndian.PutUint16(packet[20+at:], full_checksum(pseudo_header(packet), packet[20:]))
	return packet
}

func udp(source_port, destination_port uint16, payload string) []byte {
	l4 := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(l4[0:], source_port)
	binary.BigEndian.PutUint16(l4[2:], destination_port)
	binary.BigEndian.PutUint16(l4[4:], uint16(len(l4)))
	copy(l4[8:], payload)
	return l4
}

// tcp_syn is a SYN with options: NOP, NOP, MSS, then window scale.
func tcp_syn(source_port, destination_port, mss uint16) []byte {
	l4 := make([]byte, 32)
	binary.BigEndian.PutUint16(l4[0:], source_port)
	binary.BigEndian.PutUint16(l4[2:], destination_port)
	l4[12] = 8 << 4
	l4[13] = 0x02
	binary.BigEndian.PutUint16(l4[14:], 64240)
	copy(l4[20:], []byte{1, 1, 2, 4, 0, 0, 3, 3, 8, 0, 0, 0})
	binary.BigEndian.PutUint16(l4[24:], mss)
	return l4
}

func check_sums(t *testing.T, packet []byte) {
	t.Helper()
	if full_checksum(packet[:20]) != 0 {
		t.Errorf("IPv4 header checksum is wrong")
	}
	if packet[9] == Protocol_udp && binary.BigEndian.Uint16(packet[26:]) == 0 {
		return
	}
	if full_checksum(pseudo_header(packet), packet[20:]) != 0 {
		t.Errorf("transport checksum is wrong")
	}
}

func Test_rewriting_an_address_keeps_both_checksums_right(t *testing.T) {
	for name, packet := range map[string][]byte{
		"udp":     build(Protocol_udp, "192.168.1.2", "203.0.113.7", udp(7801, 27015, "hello, server")),
		"udp odd": build(Protocol_udp, "192.168.1.2", "203.0.113.7", udp(7801, 27015, "odd")),
		"tcp":     build(Protocol_tcp, "192.168.1.2", "203.0.113.7", tcp_syn(7802, 443, 1460)),
	} {
		check_sums(t, packet)
		if err := rewrite_address(packet, source_at, netip.MustParseAddr("10.66.0.2")); err != nil {
			t.Fatal(err)
		}
		check_sums(t, packet)
		if err := rewrite_address(packet, destination_at, netip.MustParseAddr("198.51.100.250")); err != nil {
			t.Fatal(err)
		}
		check_sums(t, packet)
		flow, _ := Flow_of(packet)
		if flow.Source.Addr().String() != "10.66.0.2" || flow.Destination.Addr().String() != "198.51.100.250" {
			t.Errorf("%s: flow after rewrite = %+v", name, flow)
		}
	}
}

func Test_a_udp_packet_without_a_checksum_keeps_none(t *testing.T) {
	packet := build(Protocol_udp, "192.168.1.2", "203.0.113.7", udp(7801, 27015, "x"))
	binary.BigEndian.PutUint16(packet[26:], 0)
	if err := rewrite_address(packet, source_at, netip.MustParseAddr("10.66.0.2")); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(packet[26:]) != 0 {
		t.Fatal("a missing UDP checksum was invented")
	}
	check_sums(t, packet)
}

func Test_a_later_fragment_has_no_flow(t *testing.T) {
	packet := build(Protocol_udp, "192.168.1.2", "203.0.113.7", udp(7801, 27015, "x"))
	binary.BigEndian.PutUint16(packet[6:], 185) // offset 1480
	if _, ok := Flow_of(packet); ok {
		t.Fatal("a later fragment was read as having ports")
	}
}

func Test_garbage_is_not_a_flow(t *testing.T) {
	for _, packet := range [][]byte{nil, {0x60, 0, 0, 0}, make([]byte, 19), {0x45, 0, 0, 200, 0, 0, 0, 0, 64, 17, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8}} {
		if _, ok := Flow_of(packet); ok {
			t.Errorf("%v read as a flow", packet)
		}
	}
}

func mss_of(packet []byte) uint16 { return binary.BigEndian.Uint16(packet[20+24:]) }

func Test_a_syn_mss_is_clamped_to_the_tunnel(t *testing.T) {
	packet := build(Protocol_tcp, "192.168.1.2", "203.0.113.7", tcp_syn(7802, 443, 1460))
	clamp_mss(packet, 1380)
	if mss_of(packet) != 1380 {
		t.Fatalf("mss = %d; want 1380", mss_of(packet))
	}
	check_sums(t, packet)

	small := build(Protocol_tcp, "192.168.1.2", "203.0.113.7", tcp_syn(7802, 443, 1200))
	clamp_mss(small, 1380)
	if mss_of(small) != 1200 {
		t.Fatalf("a smaller mss was raised to %d", mss_of(small))
	}
}

func Test_a_segment_that_is_not_a_syn_is_left_alone(t *testing.T) {
	l4 := tcp_syn(7802, 443, 1460)
	l4[13] = 0x10 // ACK
	packet := build(Protocol_tcp, "192.168.1.2", "203.0.113.7", l4)
	clamp_mss(packet, 1380)
	if mss_of(packet) != 1460 {
		t.Fatal("an ACK's bytes were changed")
	}
}
