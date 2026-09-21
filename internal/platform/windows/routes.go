//go:build windows

package windows

import (
	"fmt"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// find_best_route wraps winipcfg.GetIPForwardTable2, the only part of
// pinning the endpoint host route that touches the system. Everything that
// decides which row wins is pick_route, in plan.go, and is tested there.
func find_best_route(destination netip.Addr, exclude winipcfg.LUID) (route_row, bool, error) {
	family := windows.AF_INET
	if destination.Is6() {
		family = windows.AF_INET6
	}
	table, err := winipcfg.GetIPForwardTable2(winipcfg.AddressFamily(family))
	if err != nil {
		return route_row{}, false, fmt.Errorf("platform/windows: reading the route table: %w", err)
	}
	rows := make([]route_row, len(table))
	for i := range table {
		rows[i] = route_row{
			interface_luid: table[i].InterfaceLUID,
			destination:    table[i].DestinationPrefix.Prefix(),
			next_hop:       table[i].NextHop.Addr(),
		}
	}
	row, ok := pick_route(rows, destination, exclude)
	return row, ok, nil
}
