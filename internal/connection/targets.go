package connection

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/games"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/recovery"
)

// state_frozen is the control plane's reach verdict for a protocol that
// connects from this network and then stops passing traffic.
const state_frozen = "frozen"

// obfuscated_first orders obfuscated protocols ahead of plain ones.
func obfuscated_first(protocol string) int {
	if protocol == "amneziawg" {
		return 0
	}
	return 1
}

// prefer_obfuscated reorders protocols within each node's existing offer slots,
// preserving node priority.
func prefer_obfuscated(targets []recovery.Target) []recovery.Target {
	for i := range targets {
		for j := i + 1; j < len(targets); j++ {
			if targets[i].Node_id == targets[j].Node_id && obfuscated_first(targets[j].Protocol) < obfuscated_first(targets[i].Protocol) {
				targets[i], targets[j] = targets[j], targets[i]
			}
		}
	}
	return targets
}

// spoken_target keeps a usable offered connection during reevaluation. Fresh
// automatic choices prefer offered AmneziaWG on the selected node, respecting
// protocol support, mode, frozen reports and local failure cooldowns.
func (m *Manager) spoken_target(recommendation api.Recommendation) (recovery.Target, *Fault) {
	states := make([]string, 0, len(recommendation.Alternatives))
	for _, alt := range recommendation.Alternatives {
		states = append(states, alt.Protocol+"@"+alt.Node+"="+alt.State)
	}
	m.logger.Info("control plane recommendation", "node_id", recommendation.Node_id,
		"protocol", recommendation.Protocol, "reason", recommendation.Reason, "alternatives", strings.Join(states, " "))

	// A hard-pinned server must be honored; if it is not present in the
	// response, reject the recommendation instead of connecting elsewhere.
	pinned := ""
	if m.local_state.Strategy.Pinned {
		pinned = m.local_state.Strategy.Node_id
	}
	if pinned != "" && recommendation.Node_id != pinned {
		on_pinned := false
		for _, alt := range recommendation.Alternatives {
			on_pinned = on_pinned || alt.Node == pinned
		}
		if !on_pinned {
			return recovery.Target{}, &Fault{Kind: Fault_recommendation, Op: "connect",
				Err: fmt.Errorf("connection: the picked server %s was asked for, and the control plane answered with %s instead; a control plane that does not know node_id is older than this build", pinned, recommendation.Node_id)}
		}
	}

	recommended := recovery.Target{Node_id: recommendation.Node_id, Protocol: recommendation.Protocol}
	node := recommendation.Node_id
	if pinned != "" {
		node = pinned
	}
	offered := m.unfrozen(recommendation.Alternatives)
	recommended_frozen := false
	for _, alt := range recommendation.Alternatives {
		if alt.Node == recommended.Node_id && alt.Protocol == recommended.Protocol && alt.State == state_frozen {
			recommended_frozen = true
		}
	}
	if !recommended_frozen {
		offered = append([]recovery.Target{recommended}, offered...)
	}
	eligible := m.spoken(m.pinned_to_node(offered))
	usable := func(target recovery.Target) bool {
		return m.policy.Eligible(m.now(), target) && (effective_mode(m.local_state) != Tunnel_games || m.carries_alone(target.Protocol))
	}
	// A server and protocol this network sent a problem report about steps
	// aside for any other usable offer; with none, it is still used -- a
	// report says the connection was bad, not that it does not connect.
	// Reevaluation -- a goal changed while connected -- keeps an offered
	// working connection on the selected node, reported or not: a setting
	// that has nothing to do with it is no reason to drop it.
	all_eligible := eligible
	if kept := without_troubled(eligible, recommendation.Evidence, Evidence_mode(effective_mode(m.local_state))); slices.ContainsFunc(kept, usable) {
		if len(kept) < len(eligible) {
			m.logger.Info("leaving aside what this network reported or keeps losing", "kept", len(kept), "offered", len(eligible))
		}
		eligible = kept
	}
	if current := m.get_state(); current == State_connected || current == State_degraded {
		for _, target := range all_eligible {
			if target == m.current && target.Node_id == node && usable(target) {
				return target, nil
			}
		}
	}
	// Fresh automatic choices prefer offered obfuscation on this same node.
	for _, target := range eligible {
		if target.Node_id == node && target.Protocol == "amneziawg" && usable(target) {
			if target != recommended {
				m.logger.Info("using an offered alternative this build speaks", "node_id", target.Node_id, "protocol", target.Protocol)
			}
			return target, nil
		}
	}
	for _, target := range eligible {
		if target == recommended && target.Node_id == node && usable(target) {
			return target, nil
		}
	}
	for _, target := range eligible {
		if target.Node_id == node && target.Node_id == m.local_state.Last_node_id && target.Protocol == m.local_state.Last_protocol && usable(target) {
			m.logger.Info("using the offered protocol that worked last on this node", "node_id", target.Node_id, "protocol", target.Protocol)
			return target, nil
		}
	}
	for _, target := range prefer_obfuscated(eligible) {
		if usable(target) {
			m.logger.Info("using an offered alternative this build speaks", "node_id", target.Node_id, "protocol", target.Protocol)
			return target, nil
		}
	}
	frozen := []string{}
	for _, alt := range recommendation.Alternatives {
		target := recovery.Target{Node_id: alt.Node, Protocol: alt.Protocol}
		if alt.State == state_frozen && len(m.spoken(m.pinned_to_node([]recovery.Target{target}))) > 0 {
			frozen = append(frozen, alt.Protocol)
		}
	}
	if len(eligible) > 0 {
		return recovery.Target{}, &Fault{Kind: Fault_protocol_unavailable, Op: "connect", Err: fmt.Errorf("connection: offered protocols are unavailable under the current mode or local failure cooldown")}
	}

	// A protocol picked by hand that nothing usable offers is the person's
	// own choice standing in the way, and the reason has to say so: blaming
	// the build would send them looking in the wrong place.
	if picked := m.local_state.Strategy.Protocol; picked != "" {
		why := "is not offered on this server"
		if len(frozen) > 0 {
			why = "freezes on this network (it connects, then stops passing traffic)"
		}
		return recovery.Target{}, &Fault{Kind: Fault_protocol_unavailable, Op: "connect",
			Err: fmt.Errorf("connection: the protocol picked by hand, %s, %s; pick another protocol or automatic choice", picked, why)}
	}
	if len(frozen) > 0 {
		return recovery.Target{}, &Fault{Kind: Fault_protocol_unavailable, Op: "connect",
			Err: fmt.Errorf("connection: the control plane recommends %s for this network and has seen %s freeze on it (it connects, then stops passing traffic); this build speaks no protocol that gets through yet",
				recommendation.Protocol, strings.Join(slices.Compact(frozen), ", "))}
	}
	names := []string{recommendation.Protocol}
	for _, alt := range recommendation.Alternatives {
		names = append(names, alt.Protocol)
	}
	return recovery.Target{}, &Fault{Kind: Fault_protocol_unavailable, Op: "connect",
		Err: fmt.Errorf("connection: the control plane offered %s, and this build speaks none of them", strings.Join(slices.Compact(names), ", "))}
}

// troubled says whether a row of this network's evidence holds its target
// against: the person sent a problem report about it, or most attempts on
// it here ended in a failure -- WireGuard that a filter cuts seconds after
// every connect. One failure is chance, not a pattern, so it takes two.
func troubled(row api.Connection_evidence) bool {
	return row.Reported > 0 || (row.Failures >= 2 && 2*row.Failures > row.Attempts)
}

// without_troubled drops the targets the evidence for this network holds
// against, from the app's own observations in the same mode: a report made
// while only games went through says nothing about the whole machine going
// through.
func without_troubled(targets []recovery.Target, evidence []api.Connection_evidence, mode string) []recovery.Target {
	reported := map[recovery.Target]bool{}
	for _, row := range evidence {
		if row.Source == "app" && row.Mode == mode && troubled(row) {
			reported[recovery.Target{Node_id: row.Node_id, Protocol: row.Protocol}] = true
		}
	}
	if len(reported) == 0 {
		return targets
	}
	kept := targets[:0:0]
	for _, t := range targets {
		if !reported[t] {
			kept = append(kept, t)
		}
	}
	return kept
}

// carries_alone reports whether protocol can put the games through the
// tunnel alone, rather than the whole machine.
func (m *Manager) carries_alone(protocol string) bool {
	p, err := m.protocols.Get(protocol)
	if err != nil {
		return false
	}
	_, whole := p.(protocols.Whole_machine)
	return !whole
}

// unfrozen is the offers the control plane has not seen freeze on this
// network, as targets.
func (m *Manager) unfrozen(list []api.Alternative) []recovery.Target {
	targets := make([]recovery.Target, 0, len(list))
	for _, alt := range list {
		if alt.State != state_frozen {
			targets = append(targets, recovery.Target{Node_id: alt.Node, Protocol: alt.Protocol})
		}
	}
	return targets
}

// offered_now is what the last recommendation offered besides its choice,
// or, without one, the alternatives recovery holds.
func (m *Manager) offered_now() []recovery.Target {
	if m.has_recommended {
		return m.unfrozen(m.recommended.Alternatives)
	}
	return m.alternatives
}

// spoken keeps the targets whose protocol this build has an adapter for.
func (m *Manager) spoken(targets []recovery.Target) []recovery.Target {
	kept := targets[:0:0]
	for _, target := range targets {
		if _, err := m.protocols.Get(target.Protocol); err == nil {
			kept = append(kept, target)
		}
	}
	return kept
}

// recommend_request is what the control plane is asked for: the stored
// strategy, the connection a route goal measures, and the game when one
// alone is chosen -- the control plane takes one, and picking one of
// several would put the others on a node chosen for someone else.
func (m *Manager) recommend_request() api.Recommend_request {
	s := m.local_state.Strategy
	request := api.Recommend_request{
		Anonymity:         s.Anonymity,
		Bypass:            s.Bypass,
		Country:           s.Country,
		Route:             s.Route,
		Client_id:         m.local_state.Client_id,
		Preferred_node_id: s.Node_id,
	}
	if s.Pinned {
		request.Node_id = s.Node_id
		request.Preferred_node_id = ""
	}
	if m.evidence != nil {
		request.Network_context_id = m.evidence.Context_id(context.Background())
	}
	if len(m.local_state.Games) == 1 && !plays_direct(m.local_state) {
		request.Game = m.local_state.Games[0]
		if catalog, err := games.Load_catalog(); err == nil {
			request.Game = catalog.Target(request.Game, m.local_state.Game_region)
		}
	}
	return request
}

// pinned_to_node drops offers that do not match the stored node or protocol;
// with neither, every offer stands. A preferred server (Pinned == false) is open
// to any offered fallback; a picked protocol remains a constraint by itself.
func (m *Manager) pinned_to_node(list []recovery.Target) []recovery.Target {
	node, protocol := "", m.local_state.Strategy.Protocol
	if m.local_state.Strategy.Pinned {
		node = m.local_state.Strategy.Node_id
	}
	if node == "" && protocol == "" {
		return list
	}
	kept := list[:0:0]
	for _, t := range list {
		if (node == "" || t.Node_id == node) && (protocol == "" || t.Protocol == protocol) {
			kept = append(kept, t)
		}
	}
	return kept
}

func alternatives_from(list []api.Alternative) []recovery.Target {
	targets := make([]recovery.Target, 0, len(list))
	for _, alt := range list {
		if alt.State != state_frozen {
			targets = append(targets, recovery.Target{Node_id: alt.Node, Protocol: alt.Protocol})
		}
	}
	return prefer_obfuscated(targets)
}

// The recommended primary remains an offered fallback when automatic policy
// selects an alternative. Explicit frozen reports override the primary offer.
func (m *Manager) recovery_offers(recommendation api.Recommendation, chosen recovery.Target) []recovery.Target {
	primary := recovery.Target{Node_id: recommendation.Node_id, Protocol: recommendation.Protocol}
	frozen_primary := false
	for _, alt := range recommendation.Alternatives {
		if alt.Node == primary.Node_id && alt.Protocol == primary.Protocol && alt.State == state_frozen {
			frozen_primary = true
		}
	}
	offers := alternatives_from(recommendation.Alternatives)
	if !frozen_primary {
		offers = append([]recovery.Target{primary}, offers...)
	}
	seen := map[recovery.Target]bool{chosen: true}
	kept := []recovery.Target{}
	for _, candidate := range m.spoken(m.pinned_to_node(offers)) {
		if seen[candidate] || (effective_mode(m.local_state) == Tunnel_games && !m.carries_alone(candidate.Protocol)) {
			continue
		}
		seen[candidate] = true
		kept = append(kept, candidate)
	}
	// What this network reported or keeps losing is tried after the rest.
	ordered := prefer_obfuscated(kept)
	clean := without_troubled(ordered, recommendation.Evidence, Evidence_mode(effective_mode(m.local_state)))
	if len(clean) == len(ordered) {
		return ordered
	}
	for _, t := range ordered {
		if !slices.Contains(clean, t) {
			clean = append(clean, t)
		}
	}
	return clean
}
