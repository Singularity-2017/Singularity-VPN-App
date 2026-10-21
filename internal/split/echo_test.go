package split

import (
	"net/netip"
	"testing"
)

func Test_an_echo_request_has_valid_checksums(t *testing.T) {
	packet := Echo_request(netip.MustParseAddr("10.29.8.5"), netip.MustParseAddr("203.0.113.9"), 7, 3)
	if sum_of(packet[:20]) != 0 {
		t.Error("the IPv4 header checksum does not verify")
	}
	if sum_of(packet[20:]) != 0 {
		t.Error("the ICMP checksum does not verify")
	}
	if packet[9] != Protocol_icmp || packet[20] != 8 {
		t.Errorf("not an ICMP echo request: protocol %d type %d", packet[9], packet[20])
	}
}

func Test_an_echo_reply_is_read_back(t *testing.T) {
	// A reply is the request with the ends swapped and type 0.
	packet := Echo_request(netip.MustParseAddr("203.0.113.9"), netip.MustParseAddr("10.29.8.5"), 7, 3)
	packet[20] = 0
	from, id, sequence, ok := Echo_reply(packet)
	if !ok || from != netip.MustParseAddr("203.0.113.9") || id != 7 || sequence != 3 {
		t.Fatalf("got %v %d %d %v", from, id, sequence, ok)
	}
	request := Echo_request(netip.MustParseAddr("10.29.8.5"), netip.MustParseAddr("203.0.113.9"), 7, 3)
	if _, _, _, ok := Echo_reply(request); ok {
		t.Error("a request was read as a reply")
	}
}
