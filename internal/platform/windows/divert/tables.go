package divert

import (
	"encoding/binary"
	"fmt"
)

// socket_table describes one of the tables GetExtendedTcpTable and
// GetExtendedUdpTable fill: a 32-bit row count, then fixed-size rows. The
// local port is a 32-bit field whose first two bytes are the port in
// network byte order; the owning process id is a little-endian 32-bit
// field.
type socket_table struct {
	protocol    Protocol
	row_size    int
	port_offset int
	pid_offset  int
}

// The four tables this package reads, with the owner-pid row layouts:
// MIB_TCPROW_OWNER_PID, MIB_TCP6ROW_OWNER_PID, MIB_UDPROW_OWNER_PID and
// MIB_UDP6ROW_OWNER_PID.
var (
	tcp4_table = socket_table{protocol: Tcp, row_size: 24, port_offset: 8, pid_offset: 20}
	tcp6_table = socket_table{protocol: Tcp, row_size: 56, port_offset: 20, pid_offset: 52}
	udp4_table = socket_table{protocol: Udp, row_size: 12, port_offset: 4, pid_offset: 8}
	udp6_table = socket_table{protocol: Udp, row_size: 28, port_offset: 20, pid_offset: 24}
)

// parse reads a filled table. Rows owned by process 0 -- TCP connections
// in TIME_WAIT, which no process holds any more -- are left out, as are
// rows with no port.
func (t socket_table) parse(data []byte) ([]Owned, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("divert: socket table is %d bytes, shorter than its header", len(data))
	}
	count := int(binary.LittleEndian.Uint32(data[0:4]))
	if count > (len(data)-4)/t.row_size {
		return nil, fmt.Errorf("divert: socket table claims %d rows but holds %d bytes", count, len(data))
	}
	owned := make([]Owned, 0, count)
	for i := range count {
		row := data[4+i*t.row_size:][:t.row_size]
		port := binary.BigEndian.Uint16(row[t.port_offset:])
		pid := binary.LittleEndian.Uint32(row[t.pid_offset:])
		if pid == 0 || port == 0 {
			continue
		}
		owned = append(owned, Owned{Port: Port{Protocol: t.protocol, Number: port}, Process_id: pid})
	}
	return owned, nil
}
