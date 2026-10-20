//go:build windows

package divert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Library_name is the DLL this package loads, always by full path from the
// directory the caller names -- never through the DLL search order, which
// would let anything earlier on it stand in for a packet driver.
const Library_name = "WinDivert.dll"

// Layer is where in the network stack a handle sits (WINDIVERT_LAYER).
type Layer uint32

const (
	Layer_network         Layer = 0
	Layer_network_forward Layer = 1
	Layer_flow            Layer = 2
	Layer_socket          Layer = 3
	Layer_reflect         Layer = 4
)

// Flags are WinDivertOpen's flags.
type Flags uint64

const (
	Flag_sniff      Flags = 0x0001
	Flag_drop       Flags = 0x0002
	Flag_recv_only  Flags = 0x0004
	Flag_send_only  Flags = 0x0008
	Flag_no_install Flags = 0x0010
	Flag_fragments  Flags = 0x0020
)

// Events reported at the flow and socket layers.
const (
	Event_network_packet   = 0
	Event_flow_established = 1
	Event_flow_deleted     = 2
	Event_socket_bind      = 3
	Event_socket_connect   = 4
	Event_socket_listen    = 5
	Event_socket_accept    = 6
	Event_socket_close     = 7
)

// Priority bounds: higher sees a packet first.
const (
	Priority_highest = 30000
	Priority_lowest  = -30000
)

// Address is WINDIVERT_ADDRESS, 80 bytes: a timestamp, a 32-bit word of
// bit fields, a reserved word and a 64-byte union whose meaning depends on
// the layer. The bit fields are laid out low bit first, as MSVC does.
type Address struct {
	Timestamp int64
	bits      uint32
	_         uint32
	union     [64]byte
}

func init() {
	if unsafe.Sizeof(Address{}) != 80 {
		panic("divert: Address is not 80 bytes")
	}
}

func (a *Address) Layer() Layer   { return Layer(a.bits & 0xff) }
func (a *Address) Event() int     { return int(a.bits >> 8 & 0xff) }
func (a *Address) Sniffed() bool  { return a.bit(16) }
func (a *Address) Outbound() bool { return a.bit(17) }
func (a *Address) Loopback() bool { return a.bit(18) }
func (a *Address) Impostor() bool { return a.bit(19) }
func (a *Address) Ipv6() bool     { return a.bit(20) }

func (a *Address) bit(n uint) bool { return a.bits>>n&1 == 1 }

// Set_outbound sets the direction a packet is injected in.
func (a *Address) Set_outbound(outbound bool) {
	if outbound {
		a.bits |= 1 << 17
	} else {
		a.bits &^= 1 << 17
	}
}

// Set_checksums_valid marks the IPv4, TCP and UDP checksums as computed
// (bits 21-23), for a packet this program built or rewrote with correct
// sums.
func (a *Address) Set_checksums_valid() {
	a.bits |= 1<<21 | 1<<22 | 1<<23
}

// Interface is the network layer's interface and sub-interface index.
func (a *Address) Interface() (index, sub_index uint32) {
	return binary.LittleEndian.Uint32(a.union[0:4]), binary.LittleEndian.Uint32(a.union[4:8])
}

// Set_interface sets the interface an inbound packet is injected on.
func (a *Address) Set_interface(index, sub_index uint32) {
	binary.LittleEndian.PutUint32(a.union[0:4], index)
	binary.LittleEndian.PutUint32(a.union[4:8], sub_index)
}

// Socket is what the flow and socket layers report about the socket an
// event belongs to.
type Socket struct {
	Endpoint_id        uint64
	Parent_endpoint_id uint64
	Process_id         uint32
	Local_address      netip.Addr
	Remote_address     netip.Addr
	Local_port         uint16
	Remote_port        uint16
	Protocol           uint8 // IANA number: 6 TCP, 17 UDP
}

// Socket decodes the union as the flow and socket layers fill it.
func (a *Address) Socket() Socket {
	u := a.union[:]
	return Socket{
		Endpoint_id:        binary.LittleEndian.Uint64(u[0:8]),
		Parent_endpoint_id: binary.LittleEndian.Uint64(u[8:16]),
		Process_id:         binary.LittleEndian.Uint32(u[16:20]),
		Local_address:      decode_address(u[20:36]),
		Remote_address:     decode_address(u[36:52]),
		Local_port:         binary.LittleEndian.Uint16(u[52:54]),
		Remote_port:        binary.LittleEndian.Uint16(u[54:56]),
		Protocol:           u[56],
	}
}

// decode_address reads WinDivert's UINT32[4] address: four host-order
// words, least significant first, so the 16 bytes are the IPv6 address
// reversed. An IPv4 address is stored IPv4-mapped (::ffff:a.b.c.d) and
// comes back as plain IPv4.
func decode_address(field []byte) netip.Addr {
	var bytes [16]byte
	for i := range bytes {
		bytes[i] = field[15-i]
	}
	return netip.AddrFrom16(bytes).Unmap()
}

// Library is a loaded WinDivert.dll.
type Library struct {
	open, recv, send, shutdown, close *windows.LazyProc
}

// Load loads WinDivert.dll from dir and resolves every procedure this
// package calls, so a wrong or truncated DLL fails here and not halfway
// through a session.
func Load(dir string) (*Library, error) {
	dll := windows.NewLazyDLL(filepath.Join(dir, Library_name))
	if err := dll.Load(); err != nil {
		return nil, fmt.Errorf("divert: loading %s from %s: %w", Library_name, dir, err)
	}
	l := &Library{
		open:     dll.NewProc("WinDivertOpen"),
		recv:     dll.NewProc("WinDivertRecv"),
		send:     dll.NewProc("WinDivertSend"),
		shutdown: dll.NewProc("WinDivertShutdown"),
		close:    dll.NewProc("WinDivertClose"),
	}
	for _, proc := range []*windows.LazyProc{l.open, l.recv, l.send, l.shutdown, l.close} {
		if err := proc.Find(); err != nil {
			return nil, fmt.Errorf("divert: %s: %w", Library_name, err)
		}
	}
	return l, nil
}

// Handle is an open WinDivert handle.
type Handle struct {
	library *Library
	handle  uintptr
}

// Open opens a handle. The first open on a machine installs and starts the
// driver as the service "WinDivert"; Driver.Before_open must run first so
// that installation is remembered as ours.
func (l *Library) Open(filter string, layer Layer, priority int16, flags Flags) (*Handle, error) {
	text, err := windows.BytePtrFromString(filter)
	if err != nil {
		return nil, fmt.Errorf("divert: filter: %w", err)
	}
	handle, _, call_err := l.open.Call(uintptr(unsafe.Pointer(text)), uintptr(layer), uintptr(priority), uintptr(flags))
	if windows.Handle(handle) == windows.InvalidHandle {
		return nil, open_error(call_err)
	}
	return &Handle{library: l, handle: handle}, nil
}

// open_error names the failures a person at the machine can do something
// about. The two signature ones are what Memory integrity or the vulnerable
// driver blocklist would produce, and they are the reason stage 9 has a
// check on a real machine.
func open_error(err error) error {
	var errno windows.Errno
	if !errors.As(err, &errno) {
		return fmt.Errorf("divert: opening a handle: %w", err)
	}
	switch errno {
	case windows.ERROR_FILE_NOT_FOUND:
		return fmt.Errorf("divert: WinDivert64.sys is not next to %s: %w", Library_name, err)
	case windows.ERROR_ACCESS_DENIED:
		return fmt.Errorf("divert: opening a handle needs administrator: %w", err)
	case windows.ERROR_INVALID_PARAMETER:
		return fmt.Errorf("divert: the filter, layer, priority or flags were refused: %w", err)
	case windows.ERROR_INVALID_IMAGE_HASH:
		return fmt.Errorf("divert: Windows refused the driver's signature: %w", err)
	case windows.ERROR_DRIVER_BLOCKED:
		return fmt.Errorf("divert: Windows blocked the driver (Memory integrity or the vulnerable driver blocklist): %w", err)
	}
	return fmt.Errorf("divert: opening a handle: %w", err)
}

// Recv blocks until a packet or event arrives, fills address, and returns
// the packet's length in buffer. Flow and socket events carry no packet:
// buffer may be nil there. After Shutdown or Close it fails with
// ERROR_NO_DATA or ERROR_INVALID_HANDLE.
func (h *Handle) Recv(buffer []byte, address *Address) (int, error) {
	// Every pointer is converted inside the Call expression: only there
	// does the compiler keep the object alive and in place for the call.
	var length uint32
	ok, _, err := h.library.recv.Call(h.handle, uintptr(unsafe.Pointer(unsafe.SliceData(buffer))), uintptr(len(buffer)), uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(address)))
	if ok == 0 {
		return 0, fmt.Errorf("divert: receiving: %w", err)
	}
	return int(length), nil
}

// Send injects packet with address's direction and, inbound, interface.
// An outbound packet's interface is ignored: the stack routes it again by
// destination.
func (h *Handle) Send(packet []byte, address *Address) error {
	if len(packet) == 0 {
		return fmt.Errorf("divert: sending an empty packet")
	}
	var length uint32
	ok, _, err := h.library.send.Call(h.handle, uintptr(unsafe.Pointer(&packet[0])), uintptr(len(packet)), uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(address)))
	if ok == 0 {
		return fmt.Errorf("divert: sending: %w", err)
	}
	return nil
}

// Shutdown stops the handle receiving, so a goroutine blocked in Recv
// returns; queued packets are dropped. Call it before Close.
func (h *Handle) Shutdown() error {
	const shutdown_both = 3
	if ok, _, err := h.library.shutdown.Call(h.handle, shutdown_both); ok == 0 {
		return fmt.Errorf("divert: shutting down: %w", err)
	}
	return nil
}

// Shutdown_receive stops new packets being queued to the handle while
// those already queued can still be received; Recv fails with
// ERROR_NO_DATA once they are gone. A capture handle being replaced is
// retired this way, so no packet it already took is lost.
func (h *Handle) Shutdown_receive() error {
	const shutdown_recv = 1
	if ok, _, err := h.library.shutdown.Call(h.handle, shutdown_recv); ok == 0 {
		return fmt.Errorf("divert: shutting down receive: %w", err)
	}
	return nil
}

// Close closes the handle. The driver stays loaded after the last handle
// closes; Driver.Remove is what takes it off the machine.
func (h *Handle) Close() error {
	if ok, _, err := h.library.close.Call(h.handle); ok == 0 {
		return fmt.Errorf("divert: closing: %w", err)
	}
	return nil
}
