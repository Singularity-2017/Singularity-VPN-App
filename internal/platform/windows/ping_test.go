//go:build windows

package windows

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func Test_the_tunnel_adapter_is_found_by_its_name_alone(t *testing.T) {
	rows := []interface_row{
		{luid: 10, alias: "Ethernet"},
		{luid: 20, alias: "Singularity"},
		{luid: 30, alias: "singularity-backup"},
	}

	if luid, ok := pick_interface_by_alias(rows, "Singularity"); !ok || luid != 20 {
		t.Fatalf("pick_interface_by_alias(Singularity) = %d, %v; want 20, true", luid, ok)
	}
	if luid, ok := pick_interface_by_alias(rows, "Wi-Fi"); ok || luid != 0 {
		t.Fatalf("pick_interface_by_alias(Wi-Fi) = %d, %v; want nothing: no such adapter excludes nothing", luid, ok)
	}
	if _, ok := pick_interface_by_alias([]interface_row{{luid: 5, alias: ""}}, ""); ok {
		t.Fatal("an empty alias must not match an interface that has none")
	}
}

func Test_the_source_address_is_a_usable_ipv4_address_of_the_chosen_interface(t *testing.T) {
	const physical, tunnel winipcfg.LUID = 10, 20
	usable := func(luid winipcfg.LUID, s string) address_row {
		return address_row{interface_luid: luid, address: addr(t, s), preferred: true}
	}

	cases := []struct {
		name string
		rows []address_row
		want string // "" for none
	}{
		{"the address of the chosen interface, not of the tunnel", []address_row{
			usable(tunnel, "10.8.0.2"),
			usable(physical, "192.168.1.20"),
		}, "192.168.1.20"},
		{"the first of several", []address_row{
			usable(physical, "192.168.1.20"),
			usable(physical, "192.168.1.21"),
		}, "192.168.1.20"},
		{"an address that asked not to be a source is passed over", []address_row{
			{interface_luid: physical, address: addr(t, "192.168.1.19"), preferred: true, skip_as_source: true},
			usable(physical, "192.168.1.20"),
		}, "192.168.1.20"},
		{"an address still in duplicate detection is passed over", []address_row{
			{interface_luid: physical, address: addr(t, "192.168.1.19")},
			usable(physical, "192.168.1.20"),
		}, "192.168.1.20"},
		{"a self-assigned address is passed over", []address_row{
			usable(physical, "169.254.7.7"),
			usable(physical, "192.168.1.20"),
		}, "192.168.1.20"},
		{"an IPv6 address is passed over", []address_row{
			usable(physical, "2001:db8::1"),
			usable(physical, "192.168.1.20"),
		}, "192.168.1.20"},
		{"an interface with nothing usable gives nothing", []address_row{
			usable(physical, "169.254.7.7"),
			usable(tunnel, "10.8.0.2"),
		}, ""},
		{"no addresses at all", nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pick_source_address(tc.rows, physical)
			if tc.want == "" {
				if ok {
					t.Fatalf("pick_source_address() = %s, true; want nothing", got)
				}
				return
			}
			if !ok || got != addr(t, tc.want) {
				t.Fatalf("pick_source_address() = %s, %v; want %s", got, ok, tc.want)
			}
		})
	}
}

// The ICMP API wants the octets in memory order, so 192.0.2.1 is the
// little-endian reading of C0 00 02 01.
func Test_an_ipv4_address_is_handed_to_the_icmp_api_in_memory_order(t *testing.T) {
	if got, want := ipv4_to_ipaddr(addr(t, "192.0.2.1")), uint32(0x010200C0); got != want {
		t.Fatalf("ipv4_to_ipaddr(192.0.2.1) = %#x, want %#x", got, want)
	}
}

func Test_an_echo_waits_for_what_the_caller_has_left_but_never_more_than_a_second(t *testing.T) {
	cases := []struct {
		remaining time.Duration
		want      uint32
	}{
		{900 * time.Millisecond, 900},
		{time.Second, 1000},
		{30 * time.Second, 1000},
		{250 * time.Microsecond, 1},
	}
	for _, tc := range cases {
		if got := echo_timeout_ms(tc.remaining); got != tc.want {
			t.Errorf("echo_timeout_ms(%s) = %d, want %d", tc.remaining, got, tc.want)
		}
	}
}

func Test_only_ipv4_is_probed(t *testing.T) {
	p := New_pinger("Singularity")
	if rtt, err := p.Ping(context.Background(), addr(t, "2001:db8::1")); err == nil || rtt != 0 {
		t.Fatalf("Ping(IPv6) = %s, %v; want a refusal and no duration", rtt, err)
	}
	if rtt, err := p.Ping(context.Background(), netip.Addr{}); err == nil || rtt != 0 {
		t.Fatalf("Ping(no address) = %s, %v; want a refusal and no duration", rtt, err)
	}
}
