package connection

import (
	"context"
	"errors"
	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/recovery"
	"singularity-vpn-app/internal/state"
	"strings"
)

type server_lister interface {
	Servers(context.Context, string) ([]api.Server, error)
}

// Native VLESS is explicitly capability-gated, including a restored strategy.
// An older recommendation service need not yet list VLESS alternatives: the
// server catalogue is authoritative about transport support, not URI exports.
func (m *Manager) vless_target(ctx context.Context, token string, request Connect_request) (recovery.Target, *Fault) {
	unavailable := func() (recovery.Target, *Fault) {
		return recovery.Target{}, &Fault{Kind: Fault_protocol_unavailable, Op: "vless", Err: errors.New("connection: no available VLESS server in the selected scope")}
	}
	if _, err := m.protocols.Get("vless"); err != nil {
		return unavailable()
	}
	source, ok := m.control_plane.(server_lister)
	if !ok {
		return unavailable()
	}
	servers, err := source.Servers(ctx, token)
	if err != nil {
		return recovery.Target{}, classify_control_plane_error("vless_capability", err)
	}
	s := m.local_state.Strategy
	pinned := ""
	if s.Pinned {
		pinned = s.Node_id
	}
	if request.Mode == Mode_manual {
		pinned = request.Node_id
	}
	var eligible []recovery.Target
	for _, n := range servers {
		if n.Available && n.Vless_available && (pinned == "" || n.Node_id == pinned) && (s.Country == "" || strings.EqualFold(s.Country, n.Country)) {
			eligible = append(eligible, recovery.Target{Node_id: n.Node_id, Protocol: "vless"})
		}
	}
	if len(eligible) == 0 {
		return unavailable()
	}
	chosen := eligible[0]
	for _, n := range eligible {
		if n.Node_id == s.Node_id || n.Node_id == m.local_state.Last_node_id {
			chosen = n
		}
		if n.Node_id == s.Node_id {
			break
		}
	}
	m.alternatives = nil
	for _, n := range eligible {
		if n != chosen {
			m.alternatives = append(m.alternatives, n)
		}
	}
	return chosen, nil
}

func normalize_vless(local *state.State) {
	local.Tunnel_mode = Tunnel_vpn
	local.Games = nil
	local.Game_region = ""
}
