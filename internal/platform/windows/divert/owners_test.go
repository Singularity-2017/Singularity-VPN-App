package divert

import (
	"slices"
	"testing"
)

func game_pid(pid uint32) func(uint32) bool {
	return func(p uint32) bool { return p == pid }
}

func Test_a_repeated_event_changes_nothing(t *testing.T) {
	o := New_owners()
	socket := Socket{Endpoint_id: 1, Process_id: 40, Local_port: 7801, Protocol: 17}
	o.Apply(Event_socket_bind, socket)
	o.Apply(Event_socket_bind, socket)
	o.Apply(Event_socket_connect, socket)
	if got := o.Ports_of(game_pid(40)); !slices.Equal(got, []Port{{Udp, 7801}}) {
		t.Fatalf("ports = %v; want one udp/7801", got)
	}
	o.Apply(Event_socket_close, socket)
	o.Apply(Event_socket_close, socket)
	if got := o.Ports_of(game_pid(40)); len(got) != 0 {
		t.Fatalf("ports after close = %v; want none", got)
	}
}

func Test_closing_one_accepted_socket_keeps_the_listener(t *testing.T) {
	o := New_owners()
	o.Apply(Event_socket_listen, Socket{Endpoint_id: 1, Process_id: 40, Local_port: 443, Protocol: 6})
	o.Apply(Event_socket_accept, Socket{Endpoint_id: 2, Parent_endpoint_id: 1, Process_id: 40, Local_port: 443, Protocol: 6})
	o.Apply(Event_socket_close, Socket{Endpoint_id: 2})
	if got := o.Ports_of(game_pid(40)); !slices.Equal(got, []Port{{Tcp, 443}}) {
		t.Fatalf("ports = %v; want the listener's tcp/443", got)
	}
}

func Test_sockets_without_a_port_or_of_another_protocol_are_not_recorded(t *testing.T) {
	o := New_owners()
	o.Apply(Event_socket_bind, Socket{Endpoint_id: 1, Process_id: 40, Protocol: 17})
	o.Apply(Event_socket_bind, Socket{Endpoint_id: 2, Process_id: 40, Local_port: 9, Protocol: 1})
	if got := o.Processes(); len(got) != 0 {
		t.Fatalf("processes = %v; want none", got)
	}
}

func Test_the_snapshot_fills_what_events_have_not_seen(t *testing.T) {
	o := New_owners()
	o.Replace_snapshot([]Owned{{Port{Udp, 50000}, 40}, {Port{Tcp, 50001}, 41}})
	o.Apply(Event_socket_bind, Socket{Endpoint_id: 1, Process_id: 40, Local_port: 50002, Protocol: 17})
	if got := o.Ports_of(game_pid(40)); !slices.Equal(got, []Port{{Udp, 50000}, {Udp, 50002}}) {
		t.Fatalf("ports = %v", got)
	}
	if got := o.Processes(); !slices.Equal(got, []uint32{40, 41}) {
		t.Fatalf("processes = %v", got)
	}
}

func Test_an_event_wins_over_a_stale_snapshot(t *testing.T) {
	o := New_owners()
	// The game held the port when the tables were read; since then it
	// closed it and a browser took the same number.
	o.Replace_snapshot([]Owned{{Port{Udp, 50000}, 40}})
	o.Apply(Event_socket_bind, Socket{Endpoint_id: 7, Process_id: 99, Local_port: 50000, Protocol: 17})
	if got := o.Ports_of(game_pid(40)); len(got) != 0 {
		t.Fatalf("the game still owns %v", got)
	}
}

func Test_a_new_snapshot_replaces_the_old_one(t *testing.T) {
	o := New_owners()
	o.Replace_snapshot([]Owned{{Port{Udp, 50000}, 40}})
	o.Replace_snapshot([]Owned{{Port{Udp, 50003}, 40}})
	if got := o.Ports_of(game_pid(40)); !slices.Equal(got, []Port{{Udp, 50003}}) {
		t.Fatalf("ports = %v; want only the newer snapshot", got)
	}
}

func Test_ports_are_sorted_and_feed_the_filter(t *testing.T) {
	o := New_owners()
	o.Apply(Event_socket_bind, Socket{Endpoint_id: 1, Process_id: 40, Local_port: 9000, Protocol: 17})
	o.Apply(Event_socket_connect, Socket{Endpoint_id: 2, Process_id: 40, Local_port: 8000, Protocol: 6})
	o.Apply(Event_socket_bind, Socket{Endpoint_id: 3, Process_id: 40, Local_port: 7000, Protocol: 17})
	want := []Port{{Tcp, 8000}, {Udp, 7000}, {Udp, 9000}}
	got := o.Ports_of(game_pid(40))
	if !slices.Equal(got, want) {
		t.Fatalf("ports = %v; want %v", got, want)
	}
	if _, err := Outbound_ports_filter(got); err != nil {
		t.Fatal(err)
	}
}
