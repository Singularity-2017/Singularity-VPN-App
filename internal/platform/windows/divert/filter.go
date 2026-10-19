// Package divert is this program's own binding to WinDivert 2.2, a signed
// packet driver loaded from WinDivert.dll beside the executable, and the
// rules for when that driver may exist on the machine at all.
//
// WinDivert is LGPLv3; it is shipped unmodified and loaded at run time.
// See local/LOG.md, "Stage 9", for why it is here and what it must not do.
package divert

import (
	"fmt"
	"slices"
	"strings"
)

// Protocol is a transport protocol a game socket uses.
type Protocol string

const (
	Tcp Protocol = "tcp"
	Udp Protocol = "udp"
)

// Port is a local port a game process owns.
type Port struct {
	Protocol Protocol
	Number   uint16
}

// Outbound_ports_filter is the WinDivert filter that diverts outbound IPv4
// packets leaving from any of ports and nothing else. Loopback stays out:
// a game talking to its own anti-cheat service on the same machine must
// not be pulled into the tunnel. No ports is "false", a filter that matches
// nothing, rather than an empty string, which WinDivert refuses.
//
// The output is sorted so the same set always yields the same filter: a
// caller compares filters to decide whether to reopen its handle.
func Outbound_ports_filter(ports []Port) (string, error) {
	terms := make([]string, 0, len(ports))
	for _, port := range ports {
		switch port.Protocol {
		case Tcp, Udp:
		default:
			return "", fmt.Errorf("divert: unknown protocol %q", port.Protocol)
		}
		if port.Number == 0 {
			return "", fmt.Errorf("divert: port 0 is not a bound port")
		}
		terms = append(terms, fmt.Sprintf("(%s and %s.SrcPort == %d)", port.Protocol, port.Protocol, port.Number))
	}
	if len(terms) == 0 {
		return "false", nil
	}
	slices.Sort(terms)
	terms = slices.Compact(terms)
	return "outbound and !loopback and ip and (" + strings.Join(terms, " or ") + ")", nil
}
