package core

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/gameping"
	"singularity-vpn-app/internal/ipc"
)

// Game_pinger measures the round trip from this machine to the server a
// running game is playing on, through the tunnel -- or, when that server
// ignores echo, to the node only.
type Game_pinger interface {
	Ping_game(ctx context.Context) gameping.Measurement
}

// game_ping_every is how often a running game's ping is measured. A match
// sends enough in this time to tell its server from a lobby's lookups.
const game_ping_every = 5 * time.Second

// game_pings is the last ping each game had. A game keeps its number after
// its match ends; the window says how long ago it was measured.
type game_pings struct {
	mu   sync.Mutex
	last map[string]ipc.Game_ping
	// session is the tunnel the pings were measured through. Another one
	// -- a reconnect, another server -- goes another way, so its numbers
	// are dropped.
	session string
}

// follow drops the pings when the tunnel they were measured through is no
// longer the current one.
func (g *game_pings) follow(session string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if session != g.session {
		g.session = session
		clear(g.last)
	}
}

func (g *game_pings) record(ping ipc.Game_ping) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.last == nil {
		g.last = map[string]ipc.Game_ping{}
	}
	g.last[ping.Game] = ping
}

// node_game_rtt_every is how long the nodes' game round trips are kept
// before the server list is read again. The control plane keeps a check
// for fifteen minutes; a minute here adds little to that.
const node_game_rtt_every = time.Minute

// node_game_rtts is the nodes' own round trips to each game's region, as
// the server list last gave them.
type node_game_rtts struct {
	mu sync.Mutex
	at time.Time
	by map[string]map[string]float64 // node_id -> game id -> ms
}

// lookup is node's round trip to game's region, reading the server list
// through fetch when what is held is older than node_game_rtt_every. A
// failed read is not retried before then either: the list is not worth
// hammering the control plane for.
func (n *node_game_rtts) lookup(ctx context.Context, now time.Time, fetch func(context.Context) ([]api.Server, error), node, game string) (float64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.at.IsZero() || now.Sub(n.at) >= node_game_rtt_every {
		n.at = now
		n.by = nil
		if servers, err := fetch(ctx); err == nil {
			n.by = make(map[string]map[string]float64, len(servers))
			for _, s := range servers {
				if len(s.Game_rtt_ms) > 0 {
					n.by[s.Node_id] = s.Game_rtt_ms
				}
			}
		}
	}
	ms, ok := n.by[node][game]
	return ms, ok && ms > 0
}

// servers_now reads the server list for node_game_rtts.
func (a *Application) servers_now(ctx context.Context) ([]api.Server, error) {
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return nil, err
	}
	return a.control_plane.Servers(ctx, token)
}

// ms is a duration in milliseconds, to the microsecond.
func ms(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

// list is the last ping of each of the selected games that has one, by
// game id.
func (g *game_pings) list(selected []string) []ipc.Game_ping {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]ipc.Game_ping, 0, len(g.last))
	for _, id := range slices.Sorted(maps.Keys(g.last)) {
		if slices.Contains(selected, id) {
			out = append(out, g.last[id])
		}
	}
	return out
}

// measure_games asks the pinger every game_ping_every until ctx ends.
func (a *Application) measure_games(ctx context.Context) {
	ticker := time.NewTicker(game_ping_every)
	defer ticker.Stop()
	// Only a change of outcome is logged: a match would otherwise write a
	// line every few seconds.
	last_why := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			session, node := "", ""
			if s, ok := a.manager.Session(); ok {
				session, node = s.Id, s.Node_id
			}
			a.game_pings.follow(session)
			m := a.game_pinger.Ping_game(ctx)
			why, attrs := m.Why, m.Attrs
			switch {
			case !m.Measured:
			case m.To_match_server:
				a.game_pings.record(ipc.Game_ping{Game: m.Game, Rtt_ms: ms(m.Rtt), At: a.now()})
			default:
				// The first mile alone is not the game's ping, so it is
				// shown only with the node's half beside it.
				node_ms, ok := a.node_rtts.lookup(ctx, a.now(), a.servers_now, node, m.Game)
				if !ok {
					why += ", and the node has no measurement for the game"
					break
				}
				first := ms(m.Rtt)
				a.game_pings.record(ipc.Game_ping{Game: m.Game, Rtt_ms: first + node_ms, First_mile_ms: &first, Node_ms: &node_ms, At: a.now()})
				attrs = append(attrs, slog.Float64("node_ms", node_ms))
			}
			if why != last_why {
				last_why = why
				a.logger.LogAttrs(ctx, slog.LevelInfo, "game ping", append([]slog.Attr{slog.String("outcome", why)}, attrs...)...)
			}
		}
	}
}
