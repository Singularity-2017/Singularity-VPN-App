package split

import (
	"net/netip"
	"testing"
)

var tunnel = netip.MustParseAddr("10.66.0.2")

func Test_a_game_flow_goes_out_through_the_tunnel_and_its_reply_comes_back(t *testing.T) {
	s := New_splitter(tunnel, 1420)
	out := build(Protocol_udp, "192.168.1.2", "203.0.113.7", udp(7801, 27015, "ping"))
	if !s.Outbound(out, Origin{Interface_index: 12, Sub_index: 0}) {
		t.Fatal("a game packet was not taken")
	}
	check_sums(t, out)
	if flow, _ := Flow_of(out); flow.Source != netip.MustParseAddrPort("10.66.0.2:7801") {
		t.Fatalf("outbound source = %s", flow.Source)
	}

	reply := build(Protocol_udp, "203.0.113.7", "10.66.0.2", udp(27015, 7801, "pong"))
	origin, ok := s.Inbound(reply)
	if !ok || origin.Interface_index != 12 {
		t.Fatalf("Inbound = %+v, %v; want the reply, on interface 12", origin, ok)
	}
	check_sums(t, reply)
	if flow, _ := Flow_of(reply); flow.Destination != netip.MustParseAddrPort("192.168.1.2:7801") {
		t.Fatalf("reply destination = %s", flow.Destination)
	}
}

// The source is read from each packet, so a game bound to another
// program's tunnel address gets its replies at that address.
func Test_the_reply_goes_back_to_the_address_the_packet_left_with(t *testing.T) {
	s := New_splitter(tunnel, 1420)
	s.Outbound(build(Protocol_udp, "10.200.0.2", "203.0.113.7", udp(7801, 27015, "x")), Origin{Interface_index: 40})
	reply := build(Protocol_udp, "203.0.113.7", "10.66.0.2", udp(27015, 7801, "y"))
	origin, ok := s.Inbound(reply)
	if flow, _ := Flow_of(reply); !ok || origin.Interface_index != 40 || flow.Destination.Addr().String() != "10.200.0.2" {
		t.Fatalf("Inbound = %+v, %v, dst %s", origin, ok, flow.Destination)
	}
}

func Test_traffic_that_is_not_a_game_reply_goes_to_the_adapter(t *testing.T) {
	s := New_splitter(tunnel, 1420)
	s.Outbound(build(Protocol_udp, "192.168.1.2", "203.0.113.7", udp(7801, 27015, "x")), Origin{})
	for name, packet := range map[string][]byte{
		"other port":     build(Protocol_udp, "203.0.113.7", "10.66.0.2", udp(27015, 7900, "y")),
		"other protocol": build(Protocol_tcp, "203.0.113.7", "10.66.0.2", tcp_syn(443, 7801, 1460)),
		"other address":  build(Protocol_udp, "203.0.113.7", "10.66.0.9", udp(27015, 7801, "y")),
	} {
		if _, ok := s.Inbound(packet); ok {
			t.Errorf("%s: taken as a game reply", name)
		}
	}
}

func Test_a_forgotten_port_no_longer_takes_replies(t *testing.T) {
	s := New_splitter(tunnel, 1420)
	s.Outbound(build(Protocol_udp, "192.168.1.2", "203.0.113.7", udp(7801, 27015, "x")), Origin{})
	s.Retain(func(protocol uint8, port uint16) bool { return false })
	if _, ok := s.Inbound(build(Protocol_udp, "203.0.113.7", "10.66.0.2", udp(27015, 7801, "y"))); ok {
		t.Fatal("a reply to a released port was still diverted")
	}
}

func Test_outbound_syns_are_clamped_to_the_tunnel_mtu(t *testing.T) {
	s := New_splitter(tunnel, 1420)
	syn := build(Protocol_tcp, "192.168.1.2", "203.0.113.7", tcp_syn(7802, 443, 1460))
	s.Outbound(syn, Origin{})
	if mss_of(syn) != 1380 {
		t.Fatalf("mss = %d; want 1380", mss_of(syn))
	}
	check_sums(t, syn)
}

// Windows hands a captured outbound packet over with the transport sum
// left for the network card to finish. What goes into the tunnel must
// carry a real one.
func Test_an_offloaded_checksum_is_made_whole_on_the_way_out(t *testing.T) {
	s := New_splitter(tunnel, 1420)
	out := build(Protocol_udp, "192.168.1.2", "203.0.113.7", udp(7801, 27015, "ping"))
	out[26], out[27] = 0x12, 0x34 // a partial sum, not the real one
	s.Outbound(out, Origin{})
	check_sums(t, out)
}

func Test_a_tcp_connection_open_before_capture_keeps_going_direct(t *testing.T) {
	s := New_splitter(tunnel, 1420)
	l4 := tcp_syn(7802, 443, 1460)
	l4[13] = 0x18 // PSH, ACK: mid-stream
	packet := build(Protocol_tcp, "192.168.1.2", "203.0.113.7", l4)
	if s.Outbound(packet, Origin{}) {
		t.Fatal("a connection already open was moved into the tunnel")
	}
	if flow, _ := Flow_of(packet); flow.Source.Addr().String() != "192.168.1.2" {
		t.Fatal("a packet left direct was still rewritten")
	}

	s.Outbound(build(Protocol_tcp, "192.168.1.2", "203.0.113.7", tcp_syn(7803, 443, 1460)), Origin{})
	l4 = tcp_syn(7803, 443, 1460)
	l4[13] = 0x10 // the ACK after the handshake
	if !s.Outbound(build(Protocol_tcp, "192.168.1.2", "203.0.113.7", l4), Origin{}) {
		t.Fatal("a connection carried from its SYN was dropped back to direct")
	}
}

func Test_the_busiest_destination_is_where_most_udp_went(t *testing.T) {
	s := New_splitter(netip.MustParseAddr("10.29.8.5"), 1380)
	origin := Origin{Interface_index: 3}
	send := func(to string, n int) {
		for range n {
			s.Outbound(build(Protocol_udp, "192.168.1.20", to, udp(5000, 7000, "x")), origin)
		}
	}
	send("203.0.113.9", 30)
	send("198.51.100.1", 5)
	got, _, _, ok := s.Take_busiest(20)
	if !ok || got != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, _, _, ok := s.Take_busiest(1); ok {
		t.Error("counting did not start again")
	}
	send("203.0.113.9", 5)
	if _, _, _, ok := s.Take_busiest(20); ok {
		t.Error("a trickle passed the bar")
	}
}

func Test_no_destination_stands_out_among_many_or_private_ones(t *testing.T) {
	s := New_splitter(netip.MustParseAddr("10.29.8.5"), 1380)
	origin := Origin{Interface_index: 3}
	for range 60 {
		s.Outbound(build(Protocol_udp, "192.168.1.20", "192.168.1.255", udp(5000, 7000, "x")), origin)
	}
	if _, _, _, ok := s.Take_busiest(20); ok {
		t.Error("a private address was taken for a match server")
	}
	for _, to := range []string{"203.0.113.9", "198.51.100.1", "198.51.100.2"} {
		for range 30 {
			s.Outbound(build(Protocol_udp, "192.168.1.20", to, udp(5000, 7000, "x")), origin)
		}
	}
	if _, _, _, ok := s.Take_busiest(20); ok {
		t.Error("one of three equal peers was taken for a match server")
	}
}
