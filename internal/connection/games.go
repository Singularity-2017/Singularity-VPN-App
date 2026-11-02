package connection

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/games"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/recovery"
	"singularity-vpn-app/internal/state"
)

// Set_in_match records whether a game is in a match.
func (m *Manager) Set_in_match(on bool) {
	m.in_match.Store(on)
}

// In_match is whether a game is in a match, as last recorded.
func (m *Manager) In_match() bool {
	return m.in_match.Load()
}

// games_target is, among offered, a target on node whose protocol can
// carry the games alone: the protocol that last carried traffic on the
// node first, then the offers in order. False when there is none.
func (m *Manager) games_target(node string, offered []recovery.Target) (recovery.Target, bool) {
	var on_node []recovery.Target
	for _, t := range m.spoken(m.pinned_to_node(offered)) {
		if t.Node_id == node && m.carries_alone(t.Protocol) && m.policy.Eligible(m.now(), t) {
			on_node = append(on_node, t)
		}
	}
	if len(on_node) == 0 {
		return recovery.Target{}, false
	}
	// Plain WireGuard's handshake is what DPI recognises, and where it
	// does the tunnel comes up and dies seconds later; the obfuscated one
	// carries games the same way and survives it, so it goes first.
	slices.SortStableFunc(on_node, func(a, b recovery.Target) int {
		return obfuscated_first(a.Protocol) - obfuscated_first(b.Protocol)
	})
	if node == m.local_state.Last_node_id {
		for _, t := range on_node {
			if t.Protocol == m.local_state.Last_protocol && obfuscated_first(t.Protocol) == obfuscated_first(on_node[0].Protocol) {
				return t, true
			}
		}
	}
	return on_node[0], true
}

// split_for is the routing split to use for games: per-process routing for
// games that allow it, the whole machine when anonymity or bypass is asked
// for, and driver forbidden when a selected game refuses the packet driver.
// An empty split means default whole-machine routing.
func split_for(local state.State) protocols.Split {
	if len(local.Games) == 0 {
		return protocols.Split{}
	}
	catalog, err := games.Load_catalog()
	if err != nil {
		return protocols.Split{}
	}
	plan, err := games.Plan_for(local.Games, catalog)
	if err != nil {
		return protocols.Split{}
	}
	if !plan.Driver {
		return protocols.Split{Driver_forbidden: true}
	}
	if local.Strategy.Anonymity != "" || local.Strategy.Bypass {
		return protocols.Split{}
	}
	if plays_direct(local) {
		return protocols.Split{}
	}
	// Full VPN chosen: the whole machine is in the tunnel, and the game
	// with it. Games go alone only beside AntiZapret, which runs next to
	// them (see companion.go): everything the person chose works at once.
	if local.Tunnel_mode != Tunnel_antizapret {
		return protocols.Split{}
	}
	return protocols.Split{Games: plan.Per_process}
}

// plays_direct says whether the selected game is played in a region
// reached only from home: then it is not put in the tunnel at all, and the
// tunnel stays whatever the person chose for the rest.
func plays_direct(local state.State) bool {
	if len(local.Games) != 1 || local.Game_region == "" {
		return false
	}
	catalog, err := games.Load_catalog()
	if err != nil {
		return false
	}
	game, ok := catalog.Find(local.Games[0])
	if !ok {
		return false
	}
	region, ok := game.Region(local.Game_region)
	return ok && region.Direct
}

// Set_games records which games the person wants carried and the region
// they boost toward. An id the catalog does not know, or a region that is
// not one of the single selected game's, is refused before anything is
// saved.
func (m *Manager) Set_games(ids []string, region string) error {
	m.cancel_monitor_probe()
	m.op_mu.Lock()
	defer m.op_mu.Unlock()

	catalog, err := games.Load_catalog()
	if err != nil {
		return err
	}
	if _, err := games.Plan_for(ids, catalog); err != nil {
		return &Fault{Kind: Fault_configuration, Op: "set_games", Err: err}
	}
	if region != "" {
		game, _ := catalog.Find(first(ids))
		if _, ok := game.Region(region); !ok || len(ids) != 1 {
			return &Fault{Kind: Fault_configuration, Op: "set_games", Err: fmt.Errorf("connection: %q is not a region of the one selected game", region)}
		}
	}
	local, err := m.store.Load()
	if err != nil {
		return err
	}
	local.Games = slices.Clone(ids)
	local.Game_region = region
	if err := m.store.Save(local); err != nil {
		return err
	}
	m.local_state = local
	return nil
}

// Games reports what Set_games last recorded: the games and the region.
// Like Strategy, it does not wait behind a connect.
func (m *Manager) Games() ([]string, string, error) {
	local, err := m.store.Load()
	if err != nil {
		return nil, "", err
	}
	return local.Games, local.Game_region, nil
}

// first is the first id, or "" for none.
func first(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// Apply_games rebuilds a tunnel that is up when what it carries has
// changed -- the same node, a different split or a different profile of
// it. The mode cannot change under a running tunnel: games-only has no
// default route and a wrapped device, the whole machine has both. A change
// that moves the tunnel between the AntiZapret profile and the full one
// needs the other profile's config, so it is fetched first; if that fails
// the tunnel is left as it was. A rebuild that does not come back is handed
// to recovery like any other lost tunnel.
func (m *Manager) Apply_games(ctx context.Context) error {
	m.cancel_monitor_probe()
	m.op_mu.Lock()
	defer m.op_mu.Unlock()
	// A companion no longer wanted goes before the rebuild, not after it:
	// leaving the games for AntiZapret on the main tunnel would otherwise
	// put two AntiZapret tunnels, with the same routes and resolver, up at
	// once, and the new one fails its verification.
	m.drop_unwanted_companion(ctx)
	defer m.drop_unwanted_companion(ctx)

	current := m.get_state()
	if current != State_connected && current != State_degraded {
		return nil
	}
	split := split_for(m.local_state)
	session, _ := m.Session()
	want := effective_mode(m.local_state)
	// Games alone need a protocol that can carry them alone. One that
	// carries the whole machine is left for another on the same node,
	// when the node offers one; otherwise it stays, whole machine and all.
	if want == Tunnel_games && !m.carries_alone(m.current.Protocol) {
		if target, ok := m.games_target(m.current.Node_id, m.offered_now()); ok {
			m.logger.Info("games go through the tunnel alone; moving to a protocol that can carry them so",
				"node_id", target.Node_id, "from", m.current.Protocol, "protocol", target.Protocol)
			m.state_mu.Lock()
			m.session.Tunnel = want
			m.state_mu.Unlock()
			m.forget_way_back()
			m.set_state(State_switching)
			m.publish(events.Kind_switch_started, target)
			_ = m.switch_loop(ctx, m.now(), target)
			if state := m.get_state(); state == State_connected || state == State_degraded {
				return nil
			}
			return &Fault{Kind: Fault_protocol_connection, Op: "apply_games", Err: errors.New("connection: the tunnel did not come back on a protocol that carries the games alone")}
		}
	}
	if reflect.DeepEqual(split, m.current_config.Split) && want == session.Tunnel {
		return nil
	}
	m.forget_way_back()
	if profile_for(want) != profile_for(session.Tunnel) {
		token, err := m.token.Token(ctx)
		if err != nil {
			return &Fault{Kind: Fault_authentication, Op: "apply_games", Err: err}
		}
		config, err := m.control_plane.Fetch_config(ctx, token, m.local_state.Client_id, m.current.Protocol, m.current.Node_id, profile_for(want))
		if err != nil {
			return classify_control_plane_error("apply_games", err)
		}
		m.current_config = m.connection_config(m.current, config)
	} else {
		m.current_config.Split = split
	}
	m.state_mu.Lock()
	m.session.Tunnel = want
	m.state_mu.Unlock()
	if m.retry_current(ctx, true) {
		return nil
	}
	// handle_lost tries the same node once more before recovery moves
	// anywhere, and a first verification right after a rebuild fails often
	// enough on a flaky path that the second one is what usually lands. A
	// tunnel that came back is not a failure to report.
	m.handle_lost(ctx, m.now())
	if state := m.get_state(); state == State_connected || state == State_degraded {
		return nil
	}
	return &Fault{Kind: Fault_protocol_connection, Op: "apply_games", Err: errors.New("connection: the tunnel did not come back after changing what it carries")}
}
