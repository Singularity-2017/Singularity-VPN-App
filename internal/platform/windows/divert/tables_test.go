package divert

import (
	"encoding/binary"
	"slices"
	"testing"
)

// table_of builds a filled table: rows of t's size with the port written
// in network byte order and the pid little-endian, as Windows fills them.
func table_of(t socket_table, rows ...Owned) []byte {
	data := make([]byte, 4+len(rows)*t.row_size)
	binary.LittleEndian.PutUint32(data, uint32(len(rows)))
	for i, owned := range rows {
		row := data[4+i*t.row_size:]
		binary.BigEndian.PutUint16(row[t.port_offset:], owned.Port.Number)
		binary.LittleEndian.PutUint32(row[t.pid_offset:], owned.Process_id)
	}
	return data
}

func Test_each_table_reads_its_ports_and_owners(t *testing.T) {
	for name, table := range map[string]socket_table{"tcp4": tcp4_table, "tcp6": tcp6_table, "udp4": udp4_table, "udp6": udp6_table} {
		rows := []Owned{{Port{table.protocol, 7801}, 1234}, {Port{table.protocol, 443}, 4}}
		got, err := table.parse(table_of(table, rows...))
		if err != nil || !slices.Equal(got, rows) {
			t.Errorf("%s: parse = %v, %v; want %v", name, got, err, rows)
		}
	}
}

func Test_the_port_is_in_network_byte_order(t *testing.T) {
	data := make([]byte, 4+udp4_table.row_size)
	data[0] = 1
	data[4+4], data[4+5] = 0x1e, 0x79 // 7801
	data[4+8] = 7
	got, err := udp4_table.parse(data)
	if err != nil || len(got) != 1 || got[0].Port.Number != 7801 {
		t.Fatalf("parse = %v, %v; want port 7801", got, err)
	}
}

func Test_rows_nobody_owns_are_left_out(t *testing.T) {
	got, err := tcp4_table.parse(table_of(tcp4_table, Owned{Port{Tcp, 50000}, 0}, Owned{Port{Tcp, 0}, 5}))
	if err != nil || len(got) != 0 {
		t.Fatalf("parse = %v, %v; want nothing", got, err)
	}
}

func Test_a_table_claiming_more_rows_than_it_holds_is_refused(t *testing.T) {
	data := table_of(udp6_table, Owned{Port{Udp, 1}, 1})
	binary.LittleEndian.PutUint32(data, 2)
	if _, err := udp6_table.parse(data); err == nil {
		t.Fatal("a short table was read past its end")
	}
	if _, err := udp6_table.parse(data[:2]); err == nil {
		t.Fatal("a table without a header was accepted")
	}
}
