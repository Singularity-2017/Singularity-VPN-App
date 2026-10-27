package connection

import (
	"context"
	"fmt"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/state"
)

// What a tunnel carries, as a Session reports it. Tunnel_vpn and
// Tunnel_antizapret are the two profiles of a node; Tunnel_games is the
// full profile with only some games put through it.
const (
	Tunnel_vpn        = "vpn"
	Tunnel_antizapret = "antizapret"
	Tunnel_games      = "games"
)

// effective_mode decides what a connect built from local carries. Games
// alone win over everything: their packets go to arbitrary addresses, which
// an AntiZapret profile would not carry and an AntiZapret node drops.
// Anonymity forces the full tunnel too -- AntiZapret leaves most traffic
// outside it, which defeats the goal. Only then is the person's own choice
// heard, and full VPN is what is left. A protocol picked by hand that can
// only carry the whole machine cannot put games through alone; the games
// still go through, with everything else, as anonymity does it.
func effective_mode(local state.State) string {
	if len(split_for(local).Games) > 0 {
		if carries_only_whole_machine(local.Strategy.Protocol) {
			return Tunnel_vpn
		}
		return Tunnel_games
	}
	if local.Strategy.Anonymity != "" {
		return Tunnel_vpn
	}
	if local.Tunnel_mode == Tunnel_antizapret {
		return Tunnel_antizapret
	}
	return Tunnel_vpn
}

// carries_only_whole_machine names the protocol whose adapter is a
// protocols.Whole_machine. effective_mode is decided from the stored state
// alone, before any adapter is at hand, so the name is kept here; a test
// holds it to the adapter.
func carries_only_whole_machine(protocol string) bool {
	return protocol == "openvpn"
}

// profile_for is the name Fetch_config is asked for: "antizapret" for the
// AntiZapret profile and "" for the full one, which the control plane gives
// when nothing is asked.
func profile_for(tunnel string) string {
	if tunnel == Tunnel_antizapret {
		return api.Mode_antizapret
	}
	return ""
}

// Set_tunnel_mode records which profile of the node the person wants. It
// takes effect on the next connect, or at once through Apply_tunnel.
func (m *Manager) Set_tunnel_mode(mode string) error {
	if mode != Tunnel_vpn && mode != Tunnel_antizapret {
		return &Fault{Kind: Fault_configuration, Op: "set_tunnel_mode", Err: fmt.Errorf("connection: tunnel mode %q is not vpn or antizapret", mode)}
	}
	m.op_mu.Lock()
	defer m.op_mu.Unlock()

	local, err := m.store.Load()
	if err != nil {
		return err
	}
	local.Tunnel_mode = mode
	if err := m.store.Save(local); err != nil {
		return err
	}
	m.local_state = local
	return nil
}

// Tunnel_mode reports what Set_tunnel_mode last recorded, always "vpn" or
// "antizapret": a state that never recorded one is full VPN. It does not
// wait behind a connect.
func (m *Manager) Tunnel_mode() (string, error) {
	local, err := m.store.Load()
	if err != nil {
		return "", err
	}
	if local.Tunnel_mode == Tunnel_antizapret {
		return Tunnel_antizapret, nil
	}
	return Tunnel_vpn, nil
}

// Apply_tunnel rebuilds a tunnel that is up when it was built with another
// profile than the one the person now wants, on the same node. It is the
// same rebuild as Apply_games.
func (m *Manager) Apply_tunnel(ctx context.Context) error {
	return m.Apply_games(ctx)
}
