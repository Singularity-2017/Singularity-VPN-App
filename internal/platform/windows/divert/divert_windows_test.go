//go:build windows

package divert

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func Test_address_bit_fields_are_low_bit_first(t *testing.T) {
	// Layer 3 (socket), event 4 (connect), sniffed, outbound, IPv6.
	a := Address{bits: 3 | 4<<8 | 1<<16 | 1<<17 | 1<<20}
	if a.Layer() != Layer_socket || a.Event() != Event_socket_connect {
		t.Fatalf("layer %d event %d", a.Layer(), a.Event())
	}
	if !a.Sniffed() || !a.Outbound() || a.Loopback() || a.Impostor() || !a.Ipv6() {
		t.Fatalf("flags decoded wrong from %#x", a.bits)
	}
	a.Set_outbound(false)
	if a.Outbound() || a.bits != 3|4<<8|1<<16|1<<20 {
		t.Fatalf("Set_outbound(false) left %#x", a.bits)
	}
}

func Test_socket_fields_sit_where_windivert_puts_them(t *testing.T) {
	var a Address
	u := a.union[:]
	binary.LittleEndian.PutUint64(u[0:], 11)
	binary.LittleEndian.PutUint64(u[8:], 22)
	binary.LittleEndian.PutUint32(u[16:], 4242)
	// 192.168.1.5 as WinDivert stores IPv4: ::ffff:192.168.1.5 in four
	// host-order words, least significant first.
	binary.LittleEndian.PutUint32(u[20:], 0xc0a80105)
	binary.LittleEndian.PutUint32(u[24:], 0x0000ffff)
	// 2001:db8::1 the same way.
	binary.LittleEndian.PutUint32(u[36:], 0x00000001)
	binary.LittleEndian.PutUint32(u[48:], 0x20010db8)
	binary.LittleEndian.PutUint16(u[52:], 50000)
	binary.LittleEndian.PutUint16(u[54:], 443)
	u[56] = 6
	want := Socket{
		Endpoint_id: 11, Parent_endpoint_id: 22, Process_id: 4242,
		Local_address:  netip.MustParseAddr("192.168.1.5"),
		Remote_address: netip.MustParseAddr("2001:db8::1"),
		Local_port:     50000, Remote_port: 443, Protocol: 6,
	}
	if got := a.Socket(); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func Test_interface_round_trips(t *testing.T) {
	var a Address
	a.Set_interface(17, 3)
	if index, sub := a.Interface(); index != 17 || sub != 3 {
		t.Fatalf("got %d/%d", index, sub)
	}
}
