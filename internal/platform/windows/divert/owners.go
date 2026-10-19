package divert

import (
	"cmp"
	"slices"
)

// Owned is a local port and the process that owns it, as the system's own
// socket tables report it when the table is first filled.
type Owned struct {
	Port       Port
	Process_id uint32
}

// Owners is the table of which process owns which local port, built from
// socket-layer events and a snapshot of the sockets that were already open
// when the events started. It is the source of the outbound filter: a
// game's ports are the ports its processes own.
//
// Applying an event is idempotent. The socket layer reports some events
// more than once, and a socket is keyed by its endpoint id, so a repeated
// bind or connect changes nothing. Nothing assumes a port range: ephemeral
// ports differ from machine to machine.
//
// Owners is not safe for concurrent use; its one reader goroutine owns it.
type Owners struct {
	endpoints map[uint64]Owned
	snapshot  []Owned
}

func New_owners() *Owners {
	return &Owners{endpoints: map[uint64]Owned{}}
}

// protocol_of maps an IANA protocol number to the protocols this table
// tracks; anything else is not a game socket.
func protocol_of(number uint8) (Protocol, bool) {
	switch number {
	case 6:
		return Tcp, true
	case 17:
		return Udp, true
	}
	return "", false
}

// Apply records one socket-layer event. Bind, connect, listen and accept
// say the endpoint is open; close says it is gone. A socket with no local
// port yet, or of another protocol, is not recorded.
func (o *Owners) Apply(event int, socket Socket) {
	switch event {
	case Event_socket_close:
		delete(o.endpoints, socket.Endpoint_id)
	case Event_socket_bind, Event_socket_connect, Event_socket_listen, Event_socket_accept:
		protocol, ok := protocol_of(socket.Protocol)
		if !ok || socket.Local_port == 0 {
			return
		}
		o.endpoints[socket.Endpoint_id] = Owned{
			Port:       Port{Protocol: protocol, Number: socket.Local_port},
			Process_id: socket.Process_id,
		}
	}
}

// Replace_snapshot replaces the sockets known from the system's tables.
// The tables carry no endpoint ids and no close events ever match them, so
// they are replaced whole each time they are read rather than amended.
func (o *Owners) Replace_snapshot(owned []Owned) {
	o.snapshot = slices.Clone(owned)
}

// entries is everything the table knows. A port an event has reported is
// taken from the events alone: the snapshot may be older than the port's
// reuse by another process.
func (o *Owners) entries() []Owned {
	from_events := map[Port]bool{}
	all := make([]Owned, 0, len(o.endpoints)+len(o.snapshot))
	for _, owned := range o.endpoints {
		from_events[owned.Port] = true
		all = append(all, owned)
	}
	for _, owned := range o.snapshot {
		if !from_events[owned.Port] {
			all = append(all, owned)
		}
	}
	return all
}

// Processes is every process that owns at least one port, sorted: the
// caller resolves each one's image name once.
func (o *Owners) Processes() []uint32 {
	pids := []uint32{}
	for _, owned := range o.entries() {
		pids = append(pids, owned.Process_id)
	}
	slices.Sort(pids)
	return slices.Compact(pids)
}

// Ports_of is every port owned by a process the predicate accepts, sorted
// and without repeats, ready for Outbound_ports_filter.
func (o *Owners) Ports_of(accept func(process_id uint32) bool) []Port {
	ports := []Port{}
	for _, owned := range o.entries() {
		if accept(owned.Process_id) {
			ports = append(ports, owned.Port)
		}
	}
	slices.SortFunc(ports, func(a, b Port) int {
		return cmp.Or(cmp.Compare(a.Protocol, b.Protocol), cmp.Compare(a.Number, b.Number))
	})
	return slices.Compact(ports)
}
