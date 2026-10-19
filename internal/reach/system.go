package reach

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
)

// System_network is the real Network: the system resolver, the system
// dialer and crypto/tls.
type System_network struct{}

func (System_network) Resolve(ctx context.Context, host string) (string, error) {
	infos, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", errors.New("reach: the resolver returned no address")
	}
	return infos[0].IP.String(), nil
}

func (System_network) Dial(ctx context.Context, address string, port int) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(port)))
}

func (System_network) Handshake(ctx context.Context, conn net.Conn, host string) error {
	client := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	return client.HandshakeContext(ctx)
}
