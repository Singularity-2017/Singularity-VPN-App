// Package gameping carries one measurement of a game's ping from the tunnel
// that takes it to the service that shows it.
package gameping

import (
	"log/slog"
	"net/netip"
	"time"
)

// Measurement is one try at a running game's ping.
//
// Rtt is the median of the echoes answered, and To_match_server says whom
// they went to: the server the match is on, or -- when that one ignores
// echo, as PUBG's do -- the far end of the tunnel, which is only the first
// mile of the game's path. Measured is false when there is no number at
// all. Match is whether the games streamed to one server since the last
// try -- a match is on -- whether or not a number came of it. Why says what
// happened and Attrs are the numbers behind it, for the log.
type Measurement struct {
	Game            string
	Rtt             time.Duration
	To_match_server bool
	Measured        bool
	Match           bool
	// Sent is the UDP packets the games sent each destination since the
	// last try, when they were counted: what the match server was told
	// from.
	Sent  map[netip.AddrPort]int
	Why   string
	Attrs []slog.Attr
}
