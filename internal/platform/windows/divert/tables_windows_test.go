//go:build windows

package divert

import (
	"net"
	"os"
	"slices"
	"testing"
)

// Reading the system's tables changes nothing and needs no administrator,
// so the real layout is checked here against sockets this test opens.
func Test_existing_sockets_finds_our_own(t *testing.T) {
	tcp4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp4.Close()
	udp6, err := net.ListenPacket("udp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	defer udp6.Close()

	owned, err := Existing_sockets()
	if err != nil {
		t.Fatal(err)
	}
	pid := uint32(os.Getpid())
	for _, want := range []Owned{
		{Port{Tcp, uint16(tcp4.Addr().(*net.TCPAddr).Port)}, pid},
		{Port{Udp, uint16(udp6.LocalAddr().(*net.UDPAddr).Port)}, pid},
	} {
		if !slices.Contains(owned, want) {
			t.Errorf("%v is missing from %d sockets", want, len(owned))
		}
	}
}
