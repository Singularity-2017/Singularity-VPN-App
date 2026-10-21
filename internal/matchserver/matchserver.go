// Package matchserver names the server a game's match is played on, from
// how many packets the games sent each address, and keeps the player's
// word on whether it named the right one. It knows nothing of the tunnel,
// the platform or any one game: it takes per-destination packet counts and
// a game id, and what a game shows its players lives in the catalog.
package matchserver

import (
	"cmp"
	"net/netip"
	"slices"
	"time"
)

// Destination is one address and port the games sent UDP to, and how many
// packets.
type Destination struct {
	Address netip.AddrPort `json:"address"`
	Packets int            `json:"packets"`
}

// top_kept is how many destinations a detection keeps, so that other rules
// than the busiest can be tried later against the same confirmations.
const top_kept = 3

// Busiest is the first rule: the public address the games sent the most
// packets to, whatever the port, if that was at least least packets and
// more than half of all counted. A match streams many packets a second to
// one server; a lobby's lookups or a spread of peers do not single one
// out. most and total are returned either way, for the log.
func Busiest(sent map[netip.AddrPort]int, least int) (server netip.Addr, most, total int, ok bool) {
	by_address := map[netip.Addr]int{}
	for destination, n := range sent {
		by_address[destination.Addr()] += n
		total += n
	}
	for address, n := range by_address {
		// Ties go to the lower address, so the same counts always name
		// the same one.
		if n > most || n == most && address.Less(server) {
			server, most = address, n
		}
	}
	return server, most, total, most >= least && most > 0 && 2*most > total
}

// Candidate is what the detector named for one match, with the numbers it
// named it from.
type Candidate struct {
	Game   string `json:"game,omitempty"`
	Region string `json:"region,omitempty"`
	// Server is the address the games sent the most packets to over the
	// match, with the port it got the most of them on. It is the zero
	// value when two addresses tied at the top: the counts cannot tell.
	Server netip.AddrPort `json:"server"`
	// Packets went to Server's address, Runner_up to the next address,
	// Total to every address, all over the match. Their ratios are the
	// shares.
	Packets   int `json:"packets"`
	Runner_up int `json:"runner_up"`
	Total     int `json:"total"`
	// Top are the destinations, address and port, that got the most
	// packets, most first.
	Top []Destination `json:"top"`
	// First and Last are when the first and the last measurement that
	// found the games streaming were taken; each covers the few seconds
	// before it.
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

// Named reports whether the candidate names a server.
func (c Candidate) Named() bool {
	return c.Server.IsValid()
}

// open_match is a match being counted.
type open_match struct {
	game, region string
	first, last  time.Time
	sent         map[netip.AddrPort]int
	// answered is set once the player answered about this match, so it is
	// not offered again while it goes on.
	answered bool
}

func (m *open_match) add(at time.Time, game, region string, sent map[netip.AddrPort]int) {
	if game != "" {
		m.game = game
	}
	m.region = region
	if m.first.IsZero() {
		m.first = at
	}
	m.last = at
	for destination, n := range sent {
		if _, counted := m.sent[destination]; counted || len(m.sent) < sent_most {
			m.sent[destination] += n
		}
	}
}

// sent_most bounds how many destinations one match counts.
const sent_most = 4096

func (m *open_match) candidate() Candidate {
	c := Candidate{Game: m.game, Region: m.region, First: m.first, Last: m.last, Top: []Destination{}}
	by_address := map[netip.Addr]int{}
	for destination, n := range m.sent {
		by_address[destination.Addr()] += n
		c.Total += n
		c.Top = append(c.Top, Destination{Address: destination, Packets: n})
	}
	slices.SortFunc(c.Top, func(a, b Destination) int {
		if n := cmp.Compare(b.Packets, a.Packets); n != 0 {
			return n
		}
		return a.Address.Compare(b.Address)
	})
	var busiest netip.Addr
	for address, n := range by_address {
		switch {
		case n > c.Packets:
			busiest, c.Runner_up, c.Packets = address, c.Packets, n
		case n > c.Runner_up:
			c.Runner_up = n
		}
	}
	if c.Packets > 0 && c.Packets > c.Runner_up {
		// The port is the one the busiest address got the most on: the
		// first of its destinations in Top's order.
		for _, d := range c.Top {
			if d.Address.Addr() == busiest {
				c.Server = d.Address
				break
			}
		}
	}
	if len(c.Top) > top_kept {
		c.Top = c.Top[:top_kept]
	}
	return c
}
