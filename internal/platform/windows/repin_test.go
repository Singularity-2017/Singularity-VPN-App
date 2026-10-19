//go:build windows

package windows

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

type fake_route_ops struct {
	best       route_row
	found      bool
	add_err    error
	delete_err error
	calls      []string
}

func (f *fake_route_ops) best_route(netip.Addr, winipcfg.LUID, []route_row) (route_row, bool, error) {
	return f.best, f.found, nil
}

func (f *fake_route_ops) add_route(_ winipcfg.LUID, _ netip.Prefix, h netip.Addr) error {
	f.calls = append(f.calls, "add "+h.String())
	return f.add_err
}

func (f *fake_route_ops) delete_route(_ winipcfg.LUID, _ netip.Prefix, h netip.Addr) error {
	f.calls = append(f.calls, "delete "+h.String())
	return f.delete_err
}

func old_pin(t *testing.T) pinned_route {
	t.Helper()
	return pinned_route{interface_luid: 2, destination: prefix(t, "203.0.113.5/32"), next_hop: addr(t, "192.168.1.1")}
}

func Test_repin_adds_the_new_pin_before_deleting_the_old(t *testing.T) {
	ops := &fake_route_ops{found: true, best: route_row{interface_luid: 3, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "172.16.0.1")}}
	out, err := repin_routes([]pinned_route{old_pin(t)}, 1, ops)
	if err != nil {
		t.Fatalf("repin_routes: %v", err)
	}
	if want := []string{"add 172.16.0.1", "delete 192.168.1.1"}; !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls = %v, want %v", ops.calls, want)
	}
	want := pinned_route{interface_luid: 3, destination: prefix(t, "203.0.113.5/32"), next_hop: addr(t, "172.16.0.1")}
	if len(out) != 1 || out[0] != want {
		t.Fatalf("pins = %+v, want %+v", out, want)
	}
}

func Test_repin_leaves_an_unchanged_path_alone(t *testing.T) {
	ops := &fake_route_ops{found: true, best: route_row{interface_luid: 2, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "192.168.1.1")}}
	out, err := repin_routes([]pinned_route{old_pin(t)}, 1, ops)
	if err != nil || len(ops.calls) != 0 || out[0] != old_pin(t) {
		t.Fatalf("got %+v, %v, calls %v; want untouched", out, err, ops.calls)
	}
}

func Test_repin_keeps_the_old_pin_and_errors_without_a_route(t *testing.T) {
	ops := &fake_route_ops{}
	out, err := repin_routes([]pinned_route{old_pin(t)}, 1, ops)
	if err == nil || len(ops.calls) != 0 || out[0] != old_pin(t) {
		t.Fatalf("got %+v, %v, calls %v; want old pin kept and an error", out, err, ops.calls)
	}
}

func Test_repin_keeps_the_old_pin_when_adding_the_new_one_fails(t *testing.T) {
	ops := &fake_route_ops{found: true, add_err: errors.New("boom"), best: route_row{interface_luid: 3, next_hop: addr(t, "172.16.0.1")}}
	out, err := repin_routes([]pinned_route{old_pin(t)}, 1, ops)
	if err == nil || out[0] != old_pin(t) || len(ops.calls) != 1 {
		t.Fatalf("got %+v, %v, calls %v; want old pin kept, no delete", out, err, ops.calls)
	}
}

func Test_repin_remembers_an_old_pin_that_would_not_go_away(t *testing.T) {
	ops := &fake_route_ops{found: true, delete_err: errors.New("access denied"),
		best: route_row{interface_luid: 3, destination: prefix(t, "0.0.0.0/0"), next_hop: addr(t, "172.16.0.1")}}
	out, err := repin_routes([]pinned_route{old_pin(t)}, 1, ops)
	if err == nil {
		t.Fatal("repin_routes: want the delete failure reported")
	}
	moved := pinned_route{interface_luid: 3, destination: prefix(t, "203.0.113.5/32"), next_hop: addr(t, "172.16.0.1")}
	if !reflect.DeepEqual(out, []pinned_route{moved, old_pin(t)}) {
		t.Fatalf("pins = %+v, want the moved pin and the old one", out)
	}

	// The next pass must not add the moved pin again; it retries the delete.
	ops.delete_err, ops.calls = nil, nil
	out, err = repin_routes(out, 1, ops)
	if err != nil {
		t.Fatalf("second repin_routes: %v", err)
	}
	if want := []string{"delete 192.168.1.1"}; !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("calls = %v, want %v", ops.calls, want)
	}
	if !reflect.DeepEqual(out, []pinned_route{moved}) {
		t.Fatalf("pins = %+v, want only the moved pin", out)
	}
}
