package wireguard

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"singularity-vpn-app/internal/gameping"
	"singularity-vpn-app/internal/matchserver"
	"singularity-vpn-app/internal/split"
)

// A game's ping is measured to the server it is playing on, along the path
// its packets take: an ICMP echo from the tunnel address handed to
// wireguard-go beside them, its reply taken off the tunnel before the
// splitter sees it. A server that does not answer echo gives no number.

// game_echo_id marks this service's echo requests, so a reply is known to
// be ours.
const game_echo_id = 0x5356

// game_packets_least is how many UDP packets a game must have sent one
// server since the last measurement, five seconds before, for that server
// to count as a match: 30 a second. Measured on PUBG (2026-10-21): its
// menu streams about 11 a second to a lobby server for half a minute,
// which 50 let through as a match and named the wrong server; a match,
// warm-up included, streams about 70 a second to the match server.
const game_packets_least = 150

// game_echoes is how many echoes one measurement sends, and game_echo_wait
// how long each waits for its reply.
const (
	game_echoes    = 3
	game_echo_wait = time.Second
)

// game_path is what a games-only tunnel lends the measurement: where the
// games' packets go, and the replies it is waiting for.
type game_path struct {
	splitter *split.Splitter
	inject   func([]byte) bool
	// control is an address on the far end of the tunnel that answers
	// echo -- its DNS server -- asked when the match server is silent, to
	// tell a server that ignores echo from a tunnel that does not carry it.
	control netip.Addr

	mu       sync.Mutex
	sequence uint16
	waiting  map[uint16]echo_wait
}

// echo_wait is one echo in flight: whom it was sent to, and what its reply
// closes.
type echo_wait struct {
	server   netip.Addr
	answered chan struct{}
}

func new_game_path(splitter *split.Splitter) *game_path {
	return &game_path{splitter: splitter, waiting: map[uint16]echo_wait{}}
}

// take_reply takes an echo reply of ours off the tunnel. Any other packet
// is left for the splitter.
func (p *game_path) take_reply(packet []byte) bool {
	from, id, sequence, ok := split.Echo_reply(packet)
	if !ok || id != game_echo_id {
		return false
	}
	p.mu.Lock()
	wait, found := p.waiting[sequence]
	if found && wait.server == from {
		delete(p.waiting, sequence)
	} else {
		found = false
	}
	p.mu.Unlock()
	if found {
		close(wait.answered)
	}
	return true
}

// echo sends one request to server and waits for its reply.
func (p *game_path) echo(ctx context.Context, server netip.Addr) (time.Duration, bool) {
	answered := make(chan struct{})
	p.mu.Lock()
	p.sequence++
	sequence := p.sequence
	p.waiting[sequence] = echo_wait{server: server, answered: answered}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiting, sequence)
		p.mu.Unlock()
	}()

	sent := time.Now()
	if p.inject == nil || !p.inject(split.Echo_request(p.splitter.Tunnel(), server, game_echo_id, sequence)) {
		return 0, false
	}
	wait := time.NewTimer(game_echo_wait)
	defer wait.Stop()
	select {
	case <-answered:
		return time.Since(sent), true
	case <-wait.C:
	case <-ctx.Done():
	}
	return 0, false
}

// median_echo sends game_echoes echoes to address and gives the median
// time of those answered.
func (p *game_path) median_echo(ctx context.Context, address netip.Addr) (time.Duration, bool) {
	var times []time.Duration
	for range game_echoes {
		if t, answered := p.echo(ctx, address); answered {
			times = append(times, t)
		}
		if ctx.Err() != nil {
			break
		}
	}
	if len(times) == 0 {
		return 0, false
	}
	slices.Sort(times)
	return times[len(times)/2], true
}

// Ping_game measures the round trip to the server the running game is
// playing on, through the tunnel. It answers only in games-only mode, with
// exactly one selected game holding ports -- two would leave the number
// belonging to neither -- while that game streams to one server. A server
// that ignores echo leaves the first mile: the round trip to the tunnel's
// DNS server, on the node, which the caller can add the node's own
// measurement to.
func (a *Adapter) Ping_game(ctx context.Context) gameping.Measurement {
	a.mu.Lock()
	path, router := a.game_path, a.router
	a.mu.Unlock()
	if path == nil || router == nil {
		return gameping.Measurement{Why: "not games-only"}
	}
	sent := path.splitter.Take_sent()
	server, most, total, busy := matchserver.Busiest(sent, game_packets_least)
	running := router.Running_games()
	if !busy {
		return gameping.Measurement{Sent: sent, Why: "no match server", Attrs: []slog.Attr{slog.Int("running", len(running)), slog.Int("busiest", most), slog.Int("packets", total)}}
	}
	// A match is on from here, however many games run; only the number
	// needs it to be one.
	if len(running) != 1 {
		return gameping.Measurement{Match: true, Sent: sent, Why: "not one game running", Attrs: []slog.Attr{slog.Int("running", len(running))}}
	}
	game := running[0]
	if rtt, ok := path.median_echo(ctx, server); ok {
		return gameping.Measurement{Game: game, Rtt: rtt, To_match_server: true, Measured: true, Match: true, Sent: sent, Why: "measured",
			Attrs: []slog.Attr{slog.String("server", server.String())}}
	}
	attrs := []slog.Attr{slog.String("server", server.String()), slog.Int("packets", most)}
	if !path.control.IsValid() {
		return gameping.Measurement{Game: game, Match: true, Sent: sent, Why: "the match server did not answer, and the tunnel has no DNS server to ask", Attrs: attrs}
	}
	rtt, ok := path.median_echo(ctx, path.control)
	if !ok {
		return gameping.Measurement{Game: game, Match: true, Sent: sent, Why: "neither the match server nor the tunnel's DNS server answered", Attrs: attrs}
	}
	attrs = append(attrs, slog.Duration("first_mile", rtt))
	return gameping.Measurement{Game: game, Rtt: rtt, Measured: true, Match: true, Sent: sent, Why: "the match server did not answer; the first mile did", Attrs: attrs}
}
