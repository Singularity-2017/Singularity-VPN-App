//go:build windows

package windows

import (
	"net/netip"
	"testing"
)

func Test_dns_servers_value_joins_the_way_nrpt_reads_it(t *testing.T) {
	got := dns_servers_value([]netip.Addr{netip.MustParseAddr("10.29.8.1"), netip.MustParseAddr("fd00::1")})
	if got != "10.29.8.1; fd00::1" {
		t.Fatalf("got %q", got)
	}
}
