//go:build windows

package windows

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// route_ops is the part of the system repin_routes touches, so the order of
// operations can be tested without a routing table.
type route_ops interface {
	// best_route answers where destination would go now, outside the
	// tunnel and without any of the pins.
	best_route(destination netip.Addr, tunnel winipcfg.LUID, pinned []route_row) (route_row, bool, error)
	add_route(interface_luid winipcfg.LUID, destination netip.Prefix, next_hop netip.Addr) error
	delete_route(interface_luid winipcfg.LUID, destination netip.Prefix, next_hop netip.Addr) error
}

type system_route_ops struct{}

func (system_route_ops) best_route(destination netip.Addr, tunnel winipcfg.LUID, pinned []route_row) (route_row, bool, error) {
	return find_best_route(destination, tunnel, pinned)
}

func (system_route_ops) add_route(interface_luid winipcfg.LUID, destination netip.Prefix, next_hop netip.Addr) error {
	return interface_luid.AddRoute(destination, next_hop, 0)
}

// delete_route treats a route that is already gone as removed: when an
// adapter goes away the system drops its routes itself, which is the usual
// way a network change starts.
func (system_route_ops) delete_route(interface_luid winipcfg.LUID, destination netip.Prefix, next_hop netip.Addr) error {
	err := interface_luid.DeleteRoute(destination, next_hop)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}

// repin_routes moves each pin onto the route its destination would take now.
// It returns the pins as they stand afterwards, which is what the caller must
// remember so Clear removes the right rows. Per pin:
//   - same interface and next hop as now: left alone;
//   - another pin already sits on that path (an earlier move whose old row
//     could not be deleted): this one is deleted again, and kept only if
//     that fails once more;
//   - different: the new pin is added first and the old one deleted after,
//     so the endpoint is never without a pin and never falls into the tunnel;
//     an old row that will not go away stays remembered for Clear;
//   - no route outside the tunnel: the old pin is kept and an error says so.
//
// Errors are joined; one pin failing does not stop the others.
func repin_routes(pins []pinned_route, tunnel winipcfg.LUID, ops route_ops) ([]pinned_route, error) {
	rows := make([]route_row, len(pins))
	for i, pin := range pins {
		rows[i] = route_row{interface_luid: pin.interface_luid, destination: pin.destination, next_hop: pin.next_hop}
	}

	out := make([]pinned_route, 0, len(pins))
	var errs []error
	for _, pin := range pins {
		best, ok, err := ops.best_route(pin.destination.Addr(), tunnel, rows)
		if err != nil {
			errs = append(errs, err)
			out = append(out, pin)
			continue
		}
		if !ok {
			errs = append(errs, fmt.Errorf("platform/windows: no route to %s outside the tunnel, keeping the old pin", pin.destination))
			out = append(out, pin)
			continue
		}
		if best.interface_luid == pin.interface_luid && best.next_hop == pin.next_hop {
			out = append(out, pin)
			continue
		}
		moved := pinned_route{interface_luid: best.interface_luid, destination: pin.destination, next_hop: best.next_hop}
		if !slices.Contains(pins, moved) {
			if err := ops.add_route(best.interface_luid, pin.destination, best.next_hop); err != nil {
				errs = append(errs, fmt.Errorf("platform/windows: pinning endpoint host route %s on its new path: %w", pin.destination, err))
				out = append(out, pin)
				continue
			}
			out = append(out, moved)
		}
		if err := ops.delete_route(pin.interface_luid, pin.destination, pin.next_hop); err != nil {
			// Still in the table, so still remembered: Clear must get
			// another chance to take it off, or a /32 to a dead next hop
			// outlives the tunnel and wins every later lookup.
			errs = append(errs, fmt.Errorf("platform/windows: removing the old host route %s: %w", pin.destination, err))
			out = append(out, pin)
		}
	}
	return out, errors.Join(errs...)
}
