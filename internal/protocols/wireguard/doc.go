// Package wireguard parses a wg-quick configuration and renders it into the
// two things the machine needs: a UAPI configuration string for
// wireguard-go's tunnel device (see Uapi_set), and, together with
// internal/routing, the operating-system state a WireGuard tunnel requires.
//
// This package is pure: no network, no operating system, no administrator,
// and it does not import wireguard-go. It only produces the text that
// library will be given. The half of stage 3 that drives the tunnel device
// and the Windows network stack lives elsewhere and can only be verified by
// hand on a real machine; everything that can be got wrong silently is put
// here instead, where a test can see it.
package wireguard
