package wireguard

import (
	"context"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"singularity-vpn-app/internal/split"
)

// streamed is a game path whose game has just sent n UDP packets to server,
// and whose tunnel answers every echo when answer is set.
func streamed(t *testing.T, server string, n int, answer bool) *game_path {
	t.Helper()
	splitter := split.New_splitter(netip.MustParseAddr("10.29.8.5"), 1380)
	for range n {
		splitter.Outbound(udp_to(server), split.Origin{Interface_index: 3})
	}
	path := new_game_path(splitter)
	path.inject = func(request []byte) bool {
		if answer {
			reply := append([]byte(nil), request...)
			copy(reply[12:16], request[16:20])
			copy(reply[16:20], request[12:16])
			reply[20] = 0
			go path.take_reply(reply)
		}
		return true
	}
	return path
}

// udp_to is a game's UDP packet from its physical address to server.
func udp_to(server string) []byte {
	packet := make([]byte, 28)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:], 28)
	packet[8] = 64
	packet[9] = split.Protocol_udp
	copy(packet[12:16], []byte{192, 168, 1, 20})
	d := netip.MustParseAddr(server).As4()
	copy(packet[16:20], d[:])
	binary.BigEndian.PutUint16(packet[20:], 5000)
	binary.BigEndian.PutUint16(packet[22:], 7000)
	binary.BigEndian.PutUint16(packet[24:], 8)
	return packet
}

func Test_a_match_server_that_answers_gives_the_game_its_ping(t *testing.T) {
	a := &Adapter{router: &fake_router{running: []string{"pubg"}}, game_path: streamed(t, "203.0.113.9", 350, true)}
	m := a.Ping_game(context.Background())
	if !m.Measured || !m.To_match_server || m.Game != "pubg" || m.Rtt < 0 || m.Rtt > time.Second {
		t.Fatalf("got %+v", m)
	}
}

func Test_no_number_without_a_match_one_game_or_an_answer(t *testing.T) {
	cases := map[string]*Adapter{
		"no match, only a trickle": {router: &fake_router{running: []string{"pubg"}}, game_path: streamed(t, "203.0.113.9", 5, true)},
		// PUBG's menu, measured: 55 packets in five seconds to a lobby
		// server. Not a match, and not the server to name.
		"the menu's lobby stream": {router: &fake_router{running: []string{"pubg"}}, game_path: streamed(t, "203.0.113.9", 55, true)},
		"two games running":       {router: &fake_router{running: []string{"pubg", "cs2"}}, game_path: streamed(t, "203.0.113.9", 350, true)},
		"no tunnel of games":      {router: &fake_router{running: []string{"pubg"}}},
	}
	for name, a := range cases {
		if a.Ping_game(context.Background()).Measured {
			t.Errorf("%s: a ping was reported", name)
		}
	}
}

func Test_a_server_that_does_not_answer_gives_no_number(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the echo timeouts")
	}
	a := &Adapter{router: &fake_router{running: []string{"pubg"}}, game_path: streamed(t, "203.0.113.9", 350, false)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if a.Ping_game(ctx).Measured {
		t.Fatal("a ping was reported with no reply")
	}
}

func Test_a_silent_server_leaves_the_first_mile(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the echo timeouts")
	}
	path := streamed(t, "203.0.113.9", 350, false)
	path.control = netip.MustParseAddr("10.29.8.1")
	path.inject = func(request []byte) bool {
		if netip.AddrFrom4([4]byte(request[16:20])) == path.control {
			reply := append([]byte(nil), request...)
			copy(reply[12:16], request[16:20])
			copy(reply[16:20], request[12:16])
			reply[20] = 0
			go path.take_reply(reply)
		}
		return true
	}
	a := &Adapter{router: &fake_router{running: []string{"pubg"}}, game_path: path}
	m := a.Ping_game(context.Background())
	if !m.Measured || m.To_match_server || m.Game != "pubg" {
		t.Fatalf("got %+v", m)
	}
}
