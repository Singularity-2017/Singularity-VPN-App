//go:build windows

package windows

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"singularity-vpn-app/internal/platform"
)

// The ICMP helper API in iphlpapi. It is the one way to send an echo without
// a raw socket, which an ordinary process may not open and which would need
// the service's privileges for nothing.
var (
	iphlpapi_icmp           = windows.NewLazySystemDLL("iphlpapi.dll")
	proc_icmp_create_file   = iphlpapi_icmp.NewProc("IcmpCreateFile")
	proc_icmp_close_handle  = iphlpapi_icmp.NewProc("IcmpCloseHandle")
	proc_icmp_send_echo2_ex = iphlpapi_icmp.NewProc("IcmpSendEcho2Ex")
)

const (
	// ip_success is the Status of a reply that is an echo reply (IP_SUCCESS).
	// Anything else is an ICMP error from some router on the way
	// (destination unreachable, TTL expired) and says nothing about the
	// latency to the host that was asked about.
	ip_success = 0

	// ip_req_timed_out is what GetLastError says when no reply came in time.
	ip_req_timed_out syscall.Errno = 11010

	// echo_timeout_cap bounds one echo whatever the caller's context allows.
	echo_timeout_cap = time.Second

	// echo_payload is the 32 bytes ping.exe sends, so the packet looks like
	// the one every firewall already lets through.
	echo_payload = "abcdefghijklmnopqrstuvwabcdefghi"

	// io_status_block_size is room for the IO_STATUS_BLOCK the documentation
	// asks the reply buffer to hold as well; 16 is its size on 64-bit
	// Windows, the larger of the two.
	io_status_block_size = 16

	// icmp_error_size is the 8 bytes of ICMP error message the reply buffer
	// must be able to hold on top of one reply and the echoed data.
	icmp_error_size = 8
)

// icmp_echo_reply mirrors ICMP_ECHO_REPLY from icmpapi.h, with the
// IP_OPTION_INFORMATION it ends in flattened. Go lays a struct out the way
// the C compiler does on the same architecture, so the pointer-sized fields
// land where the 32-bit and the 64-bit definitions each put them.
type icmp_echo_reply struct {
	address         uint32 // the host that answered, as IPAddr
	status          uint32 // ip_success, or an IP_* error code
	round_trip_time uint32 // whole milliseconds: not used, see send_echo
	data_size       uint16
	reserved        uint16
	data            uintptr
	ttl             uint8
	tos             uint8
	flags           uint8
	options_size    uint8
	options_data    uintptr
}

// interface_row is the part of winipcfg.MibIfRow2 that finding the tunnel
// adapter needs. MibIfRow2 keeps its alias in an unexported field, so a test
// could not build one: this is the same device route_row is for pick_route.
type interface_row struct {
	luid  winipcfg.LUID
	alias string
}

// address_row is the part of winipcfg.MibUnicastIPAddressRow that choosing a
// source address needs.
type address_row struct {
	interface_luid winipcfg.LUID
	address        netip.Addr
	skip_as_source bool // the address asked not to be picked as a source
	preferred      bool // duplicate address detection is over and it is in use
}

// pinger is platform.Pinger over the ICMP helper API.
type pinger struct {
	tunnel_aliases []string
}

// New_pinger returns a platform.Pinger that sends its echoes outside the
// tunnel adapters named tunnel_aliases -- one per protocol, at most one of
// them up -- whether or not any of them exists at the moment.
func New_pinger(tunnel_aliases ...string) platform.Pinger {
	return &pinger{tunnel_aliases: tunnel_aliases}
}

// Ping sends one echo to destination and returns how long the answer took.
//
// Only IPv4 is probed. A destination that is not IPv4 is an error, and so is
// everything that is not a clean echo reply from destination itself -- see
// platform.Pinger for why a failed probe is never a zero.
func (p *pinger) Ping(ctx context.Context, destination netip.Addr) (time.Duration, error) {
	destination = destination.Unmap()
	if !destination.Is4() {
		return 0, fmt.Errorf("platform/windows: ping %s: only IPv4 can be probed", destination)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	source, err := p.source_address(destination)
	if err != nil {
		return 0, err
	}

	// The wait is taken from the deadline after the lookups above, which are
	// not free, so a caller that gave the whole call 900 ms gets an echo
	// that ends by then and not 900 ms later.
	remaining := echo_timeout_cap
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if remaining <= 0 {
		return 0, context.DeadlineExceeded
	}
	return send_echo(source, destination, echo_timeout_ms(remaining))
}

// source_address finds the address the echo is sent from, and with it the
// interface it leaves by.
//
// The echo has to go out the way the destination would be reached WITHOUT the
// tunnel, even while the tunnel is up. Left to itself, the stack picks the
// best route, and with the tunnel up that is the tunnel's 0.0.0.0/1 and
// 128.0.0.0/1: every server would then be measured through the server
// currently connected to, and all of them would read about the same.
//
// So the route is looked up here with the tunnel adapter left out
// (find_best_route, the same lookup the endpoint host route is pinned with),
// and an address of the interface that route uses is handed to the stack as
// the echo's source. Windows uses the strong host model for sending on these
// interfaces: a packet whose source is an address of interface X leaves by X,
// so the route lookup is confined to that interface's own routes and cannot
// land on the tunnel. Were the weak host send model on for the physical
// interface, the source address would not confine anything and the echo
// would still ride the tunnel; `netsh interface ipv4 show interface` lists
// it as "Weak Host Sends".
func (p *pinger) source_address(destination netip.Addr) (netip.Addr, error) {
	tunnel_luid, err := p.tunnel_luid()
	if err != nil {
		return netip.Addr{}, err
	}
	route, found, err := find_best_route(destination, tunnel_luid, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	if !found {
		return netip.Addr{}, fmt.Errorf("platform/windows: ping %s: no route to it outside the tunnel", destination)
	}

	table, err := winipcfg.GetUnicastIPAddressTable(winipcfg.AddressFamily(windows.AF_INET))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("platform/windows: reading the address table: %w", err)
	}
	rows := make([]address_row, len(table))
	for i := range table {
		rows[i] = address_row{
			interface_luid: table[i].InterfaceLUID,
			address:        table[i].Address.Addr(),
			skip_as_source: table[i].SkipAsSource,
			preferred:      table[i].DadState == winipcfg.DadStatePreferred,
		}
	}
	source, ok := pick_source_address(rows, route.interface_luid)
	if !ok {
		return netip.Addr{}, fmt.Errorf("platform/windows: ping %s: the interface it is reached by has no IPv4 address to send from", destination)
	}
	return source, nil
}

// tunnel_luid reads the LUID of whichever tunnel adapter exists, by name,
// the way the service recognises it elsewhere. No adapter by any of the
// names -- no tunnel is up, or has ever been created -- is not an error: the zero LUID that comes
// back belongs to no interface, so excluding it excludes nothing.
func (p *pinger) tunnel_luid() (winipcfg.LUID, error) {
	table, err := winipcfg.GetIfTable2Ex(winipcfg.MibIfEntryNormalWithoutStatistics)
	if err != nil {
		return 0, fmt.Errorf("platform/windows: reading the interface table: %w", err)
	}
	rows := make([]interface_row, len(table))
	for i := range table {
		rows[i] = interface_row{luid: table[i].InterfaceLUID, alias: table[i].Alias()}
	}
	for _, alias := range p.tunnel_aliases {
		if luid, ok := pick_interface_by_alias(rows, alias); ok {
			return luid, nil
		}
	}
	return 0, nil
}

// pick_interface_by_alias finds the interface called alias. An empty alias
// names nothing.
func pick_interface_by_alias(rows []interface_row, alias string) (winipcfg.LUID, bool) {
	if alias == "" {
		return 0, false
	}
	for _, row := range rows {
		if row.alias == alias {
			return row.luid, true
		}
	}
	return 0, false
}

// pick_source_address chooses the address of the interface to send an echo
// from: the first IPv4 address on it that the stack would itself use as a
// source. That leaves out an address that asked not to be one, one still in
// duplicate address detection (or deprecated), and one that is not global
// unicast -- IsGlobalUnicast is false for link-local 169.254.0.0/16, loopback,
// multicast and the unspecified address, and an interface holding only a
// self-assigned address has no route to a server anyway.
func pick_source_address(rows []address_row, interface_luid winipcfg.LUID) (netip.Addr, bool) {
	for _, row := range rows {
		if row.interface_luid != interface_luid || row.skip_as_source || !row.preferred {
			continue
		}
		if !row.address.Is4() || !row.address.IsGlobalUnicast() {
			continue
		}
		return row.address, true
	}
	return netip.Addr{}, false
}

// echo_timeout_ms is how long one echo waits: what the caller has left,
// capped at echo_timeout_cap, and at least the one millisecond the API's
// resolution allows.
func echo_timeout_ms(remaining time.Duration) uint32 {
	remaining = min(remaining, echo_timeout_cap)
	return uint32(max(remaining.Milliseconds(), 1))
}

// ipv4_to_ipaddr is the IPAddr the ICMP API takes: the four octets in memory
// order, which is what a little-endian ULONG holds. Windows only runs on
// little-endian processors.
func ipv4_to_ipaddr(addr netip.Addr) uint32 {
	octets := addr.As4()
	return binary.LittleEndian.Uint32(octets[:])
}

// send_echo sends one echo request from source to destination and waits for
// the answer, synchronously: with no event and no APC routine the call
// returns when the reply has come or timeout_ms has passed.
//
// The round trip is the wall-clock time of that call. The RoundTripTime the
// reply carries is whole milliseconds and reads 0 for a host near enough,
// which would be a zero where there is a measurement.
func send_echo(source, destination netip.Addr, timeout_ms uint32) (time.Duration, error) {
	for _, proc := range []*windows.LazyProc{proc_icmp_create_file, proc_icmp_close_handle, proc_icmp_send_echo2_ex} {
		if err := proc.Find(); err != nil {
			return 0, fmt.Errorf("platform/windows: the ICMP API is not available: %w", err)
		}
	}

	handle, _, call_err := proc_icmp_create_file.Call()
	if handle == 0 || windows.Handle(handle) == windows.InvalidHandle {
		return 0, fmt.Errorf("platform/windows: opening an ICMP handle: %w", call_err)
	}
	defer func() { _, _, _ = proc_icmp_close_handle.Call(handle) }()

	// The buffer is made of uint64 so that it is aligned for the reply
	// struct read out of it below.
	payload := []byte(echo_payload)
	reply_size := int(unsafe.Sizeof(icmp_echo_reply{})) + len(payload) + icmp_error_size + io_status_block_size
	buffer := make([]uint64, (reply_size+7)/8)

	destination_ip := ipv4_to_ipaddr(destination)
	start := time.Now()
	replies, _, call_err := proc_icmp_send_echo2_ex.Call(
		handle,
		0, // Event
		0, // ApcRoutine
		0, // ApcContext
		uintptr(ipv4_to_ipaddr(source)),
		uintptr(destination_ip),
		uintptr(unsafe.Pointer(unsafe.SliceData(payload))),
		uintptr(len(payload)),
		0, // RequestOptions: the defaults
		uintptr(unsafe.Pointer(unsafe.SliceData(buffer))),
		uintptr(len(buffer)*8),
		uintptr(timeout_ms),
	)
	elapsed := time.Since(start)

	if replies == 0 {
		if call_err == ip_req_timed_out {
			// Not "within timeout_ms": the API has been seen to give this
			// answer well before the wait it was given is up.
			return 0, fmt.Errorf("platform/windows: ping %s: no echo reply", destination)
		}
		return 0, fmt.Errorf("platform/windows: ping %s: %w", destination, call_err)
	}

	reply := (*icmp_echo_reply)(unsafe.Pointer(unsafe.SliceData(buffer)))
	if reply.status != ip_success {
		return 0, fmt.Errorf("platform/windows: ping %s: answered with ICMP status %d", destination, reply.status)
	}
	if reply.address != destination_ip {
		return 0, fmt.Errorf("platform/windows: ping %s: answered by another address", destination)
	}
	return elapsed, nil
}
