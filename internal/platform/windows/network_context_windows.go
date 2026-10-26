//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

type context_path struct {
	index        uint32
	luid         winipcfg.LUID
	metric       uint64
	source       netip.Addr
	gateway      netip.Addr
	interface_id string
	network_id   string
	dns          []netip.Addr
}

// Capture_network returns local fingerprint material and an origin-restricted
// HTTP client whose IPv4 and DNS sockets are bound to a physical interface.
// The caller must salt/hash the material before persisting it, never transmit
// it, and close idle connections after the capture. No routes are modified.
// IPv6-only and virtual-only paths deliberately return unknown context.
func Capture_network(ctx context.Context, control_plane_url string) (string, *http.Client, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	origin, err := url.Parse(control_plane_url)
	if err != nil || origin.Hostname() == "" || origin.User != nil || (origin.Scheme != "http" && origin.Scheme != "https") {
		return "", nil, errors.New("network context: invalid control plane origin")
	}
	path, err := physical_context_path(true)
	if err != nil {
		return "", nil, err
	}
	client, err := context_http_client(path, origin)
	if err != nil {
		return "", nil, err
	}
	return path.key(), client, nil
}

// Physical_path_key names the physical IPv4 path the machine uses now, the
// same material Capture_network fingerprints. Virtual adapters -- another
// VPN, Hyper-V, WSL -- rebuild their routes often without moving it, so a
// caller compares keys to tell a real network change from that churn. It is
// empty when there is no physical path. Like the material, it is never
// persisted or transmitted unhashed.
func Physical_path_key() string {
	path, err := physical_context_path(false)
	if err != nil {
		return ""
	}
	return path.key()
}

// Physical_network_key names the network itself -- interface, network and
// gateway, without the address DHCP happened to hand out -- for remembering
// what was measured on it. It needs nothing but the routing table. Empty
// when there is no physical path; never persisted unhashed.
func Physical_network_key() string {
	path, err := physical_context_path(false)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s", path.interface_id, path.network_id, path.gateway)
}

func (p context_path) key() string {
	return fmt.Sprintf("%s|%s|%s|%s", p.interface_id, p.network_id, p.gateway, p.source)
}

func context_physical_interface(row *winipcfg.MibIfRow2) bool {
	return row != nil && row.OperStatus == winipcfg.IfOperStatusUp && hardware_interface(row)
}

// hardware_luids caches hardware_interface per LUID: what an interface is
// does not change, and route notifications arrive by the hundred.
var hardware_luids sync.Map

// Is_hardware_interface says whether luid is a physical network adapter --
// Ethernet, Wi-Fi or mobile -- rather than a virtual one, whatever its state.
// One that can no longer be read and was never seen counts as one: a USB
// adapter unplugged sends its delete after it is gone, and a wrong yes
// costs one read of the routing tables where a wrong no misses a network
// change. Warm_hardware_interfaces learns every adapter present up front.
func Is_hardware_interface(luid winipcfg.LUID) bool {
	if cached, ok := hardware_luids.Load(luid); ok {
		return cached.(bool)
	}
	row, err := luid.Interface()
	if err != nil {
		return true
	}
	hardware := hardware_interface(row)
	hardware_luids.Store(luid, hardware)
	return hardware
}

// Warm_hardware_interfaces reads the interface table once and remembers
// which adapters are hardware, so a later notification about one -- its
// removal included -- is classed without reading anything.
func Warm_hardware_interfaces() error {
	table, err := winipcfg.GetIfTable2Ex(winipcfg.MibIfEntryNormalWithoutStatistics)
	if err != nil {
		return fmt.Errorf("network context: reading the interface table: %w", err)
	}
	for i := range table {
		hardware_luids.Store(table[i].InterfaceLUID, hardware_interface(&table[i]))
	}
	return nil
}

func hardware_interface(row *winipcfg.MibIfRow2) bool {
	if row == nil || row.TunnelType != winipcfg.TunnelTypeNone || row.InterfaceAndOperStatusFlags&winipcfg.IAOSFHardwareInterface == 0 {
		return false
	}
	switch row.Type {
	case winipcfg.IfTypeEthernetCSMACD, winipcfg.IfTypeIEEE80211, winipcfg.IfTypeWwanpp, winipcfg.IfTypeWwanpp2:
		return true
	}
	return false
}

// physical_context_path finds the physical path; with_dns also reads its DNS
// servers, which only Capture_network needs -- the fallback below asks the
// DNS Client service for every interface's configuration.
func physical_context_path(with_dns bool) (context_path, error) {
	routes, err := winipcfg.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		return context_path{}, errors.New("network context: cannot inspect physical routes")
	}
	addresses, err := winipcfg.GetUnicastIPAddressTable(windows.AF_INET)
	if err != nil {
		return context_path{}, errors.New("network context: cannot inspect physical addresses")
	}
	var paths []context_path
	for _, route := range routes {
		prefix := route.DestinationPrefix.Prefix()
		if !prefix.IsValid() || prefix.Bits() != 0 {
			continue
		}
		iface, err := route.InterfaceLUID.Interface()
		if err != nil || !context_physical_interface(iface) {
			continue
		}
		ip_iface, err := route.InterfaceLUID.IPInterface(windows.AF_INET)
		if err != nil {
			continue
		}
		var sources []netip.Addr
		for _, address := range addresses {
			ip := address.Address.Addr().Unmap()
			if address.InterfaceLUID == route.InterfaceLUID && !address.SkipAsSource && address.DadState == winipcfg.DadStatePreferred && ip.Is4() && ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast() {
				sources = append(sources, ip)
			}
		}
		if len(sources) == 0 {
			continue
		}
		sort.Slice(sources, func(i, j int) bool { return sources[i].Less(sources[j]) })
		paths = append(paths, context_path{
			index: iface.InterfaceIndex, luid: route.InterfaceLUID,
			metric: uint64(route.Metric) + uint64(ip_iface.Metric),
			source: sources[0], gateway: route.NextHop.Addr(),
			interface_id: iface.InterfaceGUID.String(), network_id: iface.NetworkGUID.String(),
		})
	}
	path, ok := choose_context_path(paths)
	if !ok {
		return context_path{}, errors.New("network context: no active physical IPv4 path")
	}
	if !with_dns {
		return path, nil
	}
	if dns := registry_interface_ipv4_dns(path.interface_id); len(dns) > 0 {
		path.dns = dns
		return path, nil
	}
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_INET, winipcfg.GAAFlagSkipAnycast|winipcfg.GAAFlagSkipMulticast|winipcfg.GAAFlagSkipFriendlyName)
	if err != nil {
		return context_path{}, errors.New("network context: cannot inspect physical DNS")
	}
	for _, adapter := range adapters {
		if adapter.LUID != path.luid {
			continue
		}
		for server := adapter.FirstDNSServerAddress; server != nil; server = server.Next {
			ip, ok := netip.AddrFromSlice(server.Address.IP())
			if ok && ip.Unmap().Is4() && !ip.IsUnspecified() && !ip.IsLoopback() {
				path.dns = append(path.dns, ip.Unmap())
			}
		}
	}
	return path, nil
}

// registry_interface_ipv4_dns reads the IPv4 DNS servers of a single adapter
// directly from its Tcpip\Parameters\Interfaces registry key so Capture_network
// does not have to call GetAdaptersAddresses without GAA_FLAG_SKIP_DNS_SERVER,
// which otherwise triggers a full Dnscache R_ResolverGetConfig RPC across every
// interface on the machine.
func registry_interface_ipv4_dns(interface_id string) []netip.Addr {
	if interface_id == "" {
		return nil
	}
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces\`+interface_id,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return nil
	}
	defer key.Close()

	for _, value_name := range []string{"NameServer", "DhcpNameServer"} {
		raw, _, err := key.GetStringValue(value_name)
		if err != nil {
			continue
		}
		if servers := parse_registry_ipv4_dns(raw); len(servers) > 0 {
			return servers
		}
	}
	return nil
}

func parse_registry_ipv4_dns(raw string) []netip.Addr {
	var out []netip.Addr
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		ip, err := netip.ParseAddr(field)
		if err == nil && ip.Unmap().Is4() && !ip.IsUnspecified() && !ip.IsLoopback() {
			out = append(out, ip.Unmap())
		}
	}
	return out
}

func choose_context_path(paths []context_path) (context_path, bool) {
	if len(paths) == 0 {
		return context_path{}, false
	}
	best := paths[0]
	for _, candidate := range paths[1:] {
		if candidate.metric < best.metric || (candidate.metric == best.metric && candidate.index < best.index) {
			best = candidate
		}
	}
	return best, true
}

// IP_UNICAST_IF requires an interface index in network byte order, unlike
// IPV6_UNICAST_IF. Restricting this transport to tcp4/udp4 prevents a fallback
// socket from silently using an unbound address family.
// https://learn.microsoft.com/en-us/windows/win32/winsock/ipproto-ip-socket-options
func context_socket_control(index uint32) func(string, string, syscall.RawConn) error {
	return func(network, _ string, conn syscall.RawConn) error {
		if (network != "tcp4" && network != "udp4") || index == 0 {
			return errors.New("network context: unsupported direct socket")
		}
		var socket_error error
		err := conn.Control(func(fd uintptr) {
			const ip_unicast_if = 31
			socket_error = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ip_unicast_if, int(bits.ReverseBytes32(index)))
		})
		return errors.Join(err, socket_error)
	}
}

func context_http_client(path context_path, origin *url.URL) (*http.Client, error) {
	host := origin.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if !ip.Is4() {
			return nil, errors.New("network context: IPv6-only origin is unsupported")
		}
	} else if len(path.dns) == 0 {
		return nil, errors.New("network context: physical DNS is unavailable")
	}
	port := origin.Port()
	if port == "" {
		if origin.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	allowed := net.JoinHostPort(host, port)
	control := context_socket_control(path.index)
	resolver := &net.Resolver{PreferGo: true}
	resolver.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var last_error error
		for _, server := range path.dns {
			dialer := &net.Dialer{Timeout: 2 * time.Second, Control: control}
			if strings.HasPrefix(network, "tcp") {
				dialer.LocalAddr = &net.TCPAddr{IP: net.IP(path.source.AsSlice())}
				network = "tcp4"
			} else {
				dialer.LocalAddr = &net.UDPAddr{IP: net.IP(path.source.AsSlice())}
				network = "udp4"
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(server.String(), "53"))
			if err == nil {
				return conn, nil
			}
			last_error = err
		}
		if last_error == nil {
			last_error = errors.New("network context: physical DNS is unavailable")
		}
		return nil, last_error
	}
	dialer := &net.Dialer{Timeout: 4 * time.Second, Control: control, Resolver: resolver, LocalAddr: &net.TCPAddr{IP: net.IP(path.source.AsSlice())}}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _ string, address string) (net.Conn, error) {
			if !strings.EqualFold(address, allowed) {
				return nil, errors.New("network context: origin changed")
			}
			return dialer.DialContext(ctx, "tcp4", address)
		},
		TLSHandshakeTimeout: 4 * time.Second, ResponseHeaderTimeout: 4 * time.Second,
		DisableKeepAlives: true,
	}
	return &http.Client{Transport: transport, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
