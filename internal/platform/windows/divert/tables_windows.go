//go:build windows

package divert

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi               = windows.NewLazySystemDLL("iphlpapi.dll")
	get_extended_tcp_table = iphlpapi.NewProc("GetExtendedTcpTable")
	get_extended_udp_table = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	tcp_table_owner_pid_all = 5
	udp_table_owner_pid     = 1
)

// Existing_sockets reads which process owns which TCP and UDP port right
// now, IPv4 and IPv6. The socket layer reports only what happens after a
// handle opens; this is what was already open before.
func Existing_sockets() ([]Owned, error) {
	reads := []struct {
		proc   *windows.LazyProc
		family uint32
		class  uint32
		table  socket_table
	}{
		{get_extended_tcp_table, windows.AF_INET, tcp_table_owner_pid_all, tcp4_table},
		{get_extended_tcp_table, windows.AF_INET6, tcp_table_owner_pid_all, tcp6_table},
		{get_extended_udp_table, windows.AF_INET, udp_table_owner_pid, udp4_table},
		{get_extended_udp_table, windows.AF_INET6, udp_table_owner_pid, udp6_table},
	}
	var all []Owned
	for _, read := range reads {
		data, err := read_table(read.proc, read.family, read.class)
		if err != nil {
			return nil, err
		}
		owned, err := read.table.parse(data)
		if err != nil {
			return nil, err
		}
		all = append(all, owned...)
	}
	return all, nil
}

// read_table calls one of the GetExtended*Table functions until the buffer
// is large enough: the table can grow between the call that sizes it and
// the call that fills it.
func read_table(proc *windows.LazyProc, family, class uint32) ([]byte, error) {
	size := uint32(16 * 1024)
	for range 8 {
		buffer := make([]byte, size)
		code, _, _ := proc.Call(uintptr(unsafe.Pointer(unsafe.SliceData(buffer))), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), uintptr(class), 0)
		switch err := windows.Errno(code); {
		case code == 0:
			// The row count in the header bounds the parse; size is not
			// promised to shrink to the filled length on success.
			return buffer, nil
		case errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER):
			size += 4 * 1024
		default:
			return nil, fmt.Errorf("divert: %s: %w", proc.Name, err)
		}
	}
	return nil, fmt.Errorf("divert: %s: the table kept outgrowing its buffer", proc.Name)
}
