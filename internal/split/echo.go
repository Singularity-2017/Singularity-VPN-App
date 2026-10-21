package split

import (
	"encoding/binary"
	"net/netip"
)

// Protocol_icmp is IANA's number for ICMP over IPv4.
const Protocol_icmp = 1

// echo_payload is what every echo request carries: enough to look like a
// ping and nothing that identifies the machine.
var echo_payload = []byte("singularity")

// Echo_request builds an ICMP echo request from source to destination, as
// a packet the tunnel can carry straight away: the path a game's packets
// take, so its answer measures what the game's packets go through.
func Echo_request(source, destination netip.Addr, id, sequence uint16) []byte {
	packet := make([]byte, 20+8+len(echo_payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64 // TTL
	packet[9] = Protocol_icmp
	s, d := source.As4(), destination.As4()
	copy(packet[12:16], s[:])
	copy(packet[16:20], d[:])
	binary.BigEndian.PutUint16(packet[10:12], sum_of(packet[:20]))

	icmp := packet[20:]
	icmp[0] = 8 // echo request
	binary.BigEndian.PutUint16(icmp[4:6], id)
	binary.BigEndian.PutUint16(icmp[6:8], sequence)
	copy(icmp[8:], echo_payload)
	binary.BigEndian.PutUint16(icmp[2:4], sum_of(icmp))
	return packet
}

// Echo_reply reads an ICMP echo reply: who answered, and the id and
// sequence of the request it answers. Anything else is not one.
func Echo_reply(packet []byte) (from netip.Addr, id, sequence uint16, ok bool) {
	h, err := parse_ipv4(packet)
	if err != nil || h.protocol != Protocol_icmp || !h.first_fragment {
		return netip.Addr{}, 0, 0, false
	}
	icmp := packet[h.header_length:binary.BigEndian.Uint16(packet[2:4])]
	if len(icmp) < 8 || icmp[0] != 0 {
		return netip.Addr{}, 0, 0, false
	}
	from = netip.AddrFrom4([4]byte(packet[12:16]))
	return from, binary.BigEndian.Uint16(icmp[4:6]), binary.BigEndian.Uint16(icmp[6:8]), true
}
