//go:build windows

package vless

import (
	"context"
	"errors"
	"math/bits"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/windows"
)

// Dial_probe cannot succeed through a different VPN or the physical default.
// Both the source address and IP_UNICAST_IF belong to this Xray TUN instance.
func (a *Adapter) Dial_probe(ctx context.Context, network, address string) (net.Conn, error) {
	a.mu.Lock()
	s := a.current
	a.mu.Unlock()
	if s == nil || !a.Status().Running {
		return nil, errors.New("vless: probe tunnel is down")
	}
	target, err := netip.ParseAddrPort(address)
	if err != nil || !target.Addr().Is4() || network != "tcp" {
		return nil, errors.New("vless: probe requires an IPv4 TCP target")
	}
	iface, err := net.InterfaceByName(adapter_name)
	if err != nil || iface.Index <= 0 {
		return nil, errors.New("vless: probe interface is unavailable")
	}
	dialer := &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: net.ParseIP(tun_ipv4)},
		Control: func(network, _ string, raw syscall.RawConn) error {
			if network != "tcp4" {
				return errors.New("vless: probe address family changed")
			}
			var socket_error error
			err := raw.Control(func(fd uintptr) {
				const ip_unicast_if = 31
				socket_error = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ip_unicast_if, int(bits.ReverseBytes32(uint32(iface.Index))))
			})
			return errors.Join(err, socket_error)
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp4", address)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	same := a.current == s
	a.mu.Unlock()
	if !same || !a.Status().Running {
		conn.Close()
		return nil, errors.New("vless: probe session changed")
	}
	return conn, nil
}
