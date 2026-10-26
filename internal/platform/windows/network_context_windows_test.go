//go:build windows

package windows

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"testing"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func Test_context_rejects_virtual_and_inactive_interfaces(t *testing.T) {
	physical := winipcfg.MibIfRow2{Type: winipcfg.IfTypeEthernetCSMACD, OperStatus: winipcfg.IfOperStatusUp, InterfaceAndOperStatusFlags: winipcfg.IAOSFHardwareInterface}
	if !context_physical_interface(&physical) {
		t.Fatal("active physical interface rejected")
	}
	virtual := physical
	virtual.InterfaceAndOperStatusFlags = 0
	if context_physical_interface(&virtual) {
		t.Fatal("virtual Ethernet must not identify a subscriber network")
	}
	tunnel := physical
	tunnel.Type = winipcfg.IfTypeTunnel
	if context_physical_interface(&tunnel) {
		t.Fatal("tunnel must not identify a subscriber network")
	}
	down := physical
	down.OperStatus = winipcfg.IfOperStatusDown
	if context_physical_interface(&down) {
		t.Fatal("inactive path accepted")
	}
}

func Test_context_chooses_physical_path_by_combined_metric(t *testing.T) {
	path, ok := choose_context_path([]context_path{{index: 8, metric: 40}, {index: 12, metric: 10}, {index: 9, metric: 10}})
	if !ok || path.index != 9 {
		t.Fatalf("unexpected path: %+v", path)
	}
	if _, ok := choose_context_path(nil); ok {
		t.Fatal("invented path")
	}
}

func Test_context_transport_never_uses_proxy_or_other_origin(t *testing.T) {
	origin, _ := url.Parse("https://192.0.2.10")
	client, err := context_http_client(context_path{index: 1, source: netip.MustParseAddr("192.0.2.20")}, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("context transport may use a VPN proxy")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "192.0.2.11:443"); err == nil {
		t.Fatal("foreign origin was allowed")
	}
	if err := client.CheckRedirect(&http.Request{}, nil); err != http.ErrUseLastResponse {
		t.Fatal("redirect could change context origin")
	}
}

func Test_context_requires_physical_dns_for_hostname(t *testing.T) {
	origin, _ := url.Parse("https://control.example")
	if _, err := context_http_client(context_path{}, origin); err == nil {
		t.Fatal("must not silently use system DNS through VPN")
	}
	origin, _ = url.Parse("https://[2001:db8::1]")
	if _, err := context_http_client(context_path{}, origin); err == nil {
		t.Fatal("must not silently use an unbound address family")
	}
}

func Test_parse_registry_ipv4_dns_splits_and_filters_addresses(t *testing.T) {
	got := parse_registry_ipv4_dns("192.168.1.1, 1.1.1.1;127.0.0.1 0.0.0.0 2001:4860:4860::8888 8.8.8.8")
	want := []netip.Addr{
		netip.MustParseAddr("192.168.1.1"),
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("8.8.8.8"),
	}
	if len(got) != len(want) {
		t.Fatalf("parse_registry_ipv4_dns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parse_registry_ipv4_dns[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func Test_capture_network_cancellation_precedes_inspection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Capture_network(ctx, "https://192.0.2.1"); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}

func Test_context_socket_binding_on_windows(t *testing.T) {
	path, err := physical_context_path(false)
	if err != nil {
		t.Skip("no physical IPv4 path for socket-only verification")
	}
	fd, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.Closesocket(fd)
	raw := &test_context_raw_conn{fd: uintptr(fd)}
	if err := context_socket_control(path.index)("udp4", "", raw); err != nil {
		t.Fatal(err)
	}
	value, err := windows.GetsockoptInt(fd, windows.IPPROTO_IP, 31)
	if err != nil || uint32(value) != path.index {
		t.Fatalf("bound interface=%d, want=%d, err=%v", value, path.index, err)
	}
	if err := context_socket_control(path.index)("tcp6", "", raw); err == nil {
		t.Fatal("unbound IPv6 accepted")
	}
}

type test_context_raw_conn struct{ fd uintptr }

func (c *test_context_raw_conn) Control(f func(uintptr)) error  { f(c.fd); return nil }
func (c *test_context_raw_conn) Read(func(uintptr) bool) error  { panic("unexpected Read") }
func (c *test_context_raw_conn) Write(func(uintptr) bool) error { panic("unexpected Write") }

func Test_warming_classifies_every_adapter_present(t *testing.T) {
	if err := Warm_hardware_interfaces(); err != nil {
		t.Skipf("interface table unavailable: %v", err)
	}
	table, err := winipcfg.GetIfTable2Ex(winipcfg.MibIfEntryNormalWithoutStatistics)
	if err != nil {
		t.Skipf("interface table unavailable: %v", err)
	}
	for i := range table {
		if _, ok := hardware_luids.Load(table[i].InterfaceLUID); !ok {
			t.Fatalf("adapter %q was not classified up front", table[i].Alias())
		}
	}
	// An adapter already gone and never seen may have been a physical one
	// unplugged: it counts, so its removal is not missed.
	if !Is_hardware_interface(winipcfg.LUID(0x7fff_0000_dead_0001)) {
		t.Fatal("an unreadable, unknown adapter was taken for a virtual one")
	}
}
