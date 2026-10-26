package connection

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/events"
)

// Profile_book is the part of the control plane that lists and names the
// account's profiles. A control plane without it is still a control plane:
// then every installation provisions its own, as before.
type Profile_book interface {
	Profiles(ctx context.Context, token string) ([]api.Account_profile, error)
	Label_profile(ctx context.Context, token, client_id, label string) (string, error)
}

// pc_label is what a profile this application provisions is called, and
// what it looks for before provisioning: "PC", or "PC 2", "PC 3" when an
// account has several computers.
const pc_label = "PC"

// is_pc says whether a label is one this application gives.
func is_pc(label string) bool {
	label = strings.TrimSpace(label)
	if strings.EqualFold(label, pc_label) {
		return true
	}
	rest, ok := strings.CutPrefix(strings.ToUpper(label), pc_label+" ")
	if !ok {
		return false
	}
	if rest == "" || strings.Trim(rest, "0123456789") != "" || rest[0] == '0' {
		return false
	}
	n, err := strconv.Atoi(rest)
	return err == nil && n > 1
}

// pick_free_pc is a PC profile no device is connected with: first one the
// control plane saw offline, then one it could not poll. Taking an unknown
// one may move another device off -- a profile in use on two devices moves
// the older one -- which is still better than a slot spent for nothing.
func pick_free_pc(profiles []api.Account_profile) (string, bool) {
	for _, p := range profiles {
		if is_pc(p.Label) && p.Online != nil && !*p.Online {
			return p.Id, true
		}
	}
	for _, p := range profiles {
		if is_pc(p.Label) && p.Online == nil {
			return p.Id, true
		}
	}
	return "", false
}

// next_pc_label is the first PC label no profile of the account carries.
func next_pc_label(profiles []api.Account_profile) string {
	taken := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		taken[strings.ToUpper(strings.TrimSpace(p.Label))] = true
	}
	if !taken[pc_label] {
		return pc_label
	}
	for n := 2; ; n++ {
		if label := pc_label + " " + strconv.Itoa(n); !taken[label] {
			return label
		}
	}
}

// free_pc asks the control plane for a PC profile to take. Any failure to
// ask is "none": provisioning goes on as it did before there was a choice.
func (m *Manager) free_pc(ctx context.Context, token string) (string, bool) {
	book, ok := m.control_plane.(Profile_book)
	if !ok {
		return "", false
	}
	profiles, err := book.Profiles(ctx, token)
	if err != nil {
		m.logger.Warn("could not list the account's profiles; provisioning a new one", "err", err)
		return "", false
	}
	return pick_free_pc(profiles)
}

// name_new_pc names a profile just provisioned, so the next installation
// on this account finds it. It is cosmetic to the control plane, so a
// failure is logged and the connect goes on.
func (m *Manager) name_new_pc(ctx context.Context, token, client_id string) {
	book, ok := m.control_plane.(Profile_book)
	if !ok {
		return
	}
	profiles, err := book.Profiles(ctx, token)
	if err != nil {
		m.logger.Warn("could not list the account's profiles to name the new one", "err", err)
		return
	}
	label := next_pc_label(profiles)
	if _, err := book.Label_profile(ctx, token, client_id, label); err != nil {
		m.logger.Warn("could not name the new profile", "label", label, "err", err)
	}
}

// Client_id is the profile this installation connects with; "" before the
// first connect provisions or takes one.
func (m *Manager) Client_id() (string, error) {
	local, err := m.store.Load()
	if err != nil {
		return "", err
	}
	return local.Client_id, nil
}

// left_profile_trace is how long a profile this installation stopped
// connecting with can still read as connected. The control plane counts a
// WireGuard peer online while its last handshake is under three minutes
// old, and polls each node every thirty seconds; an OpenVPN client stays
// listed until the server's keepalive gives up on it.
const left_profile_trace = 4 * time.Minute

// Left_recently says whether client_id is the profile this installation
// stopped connecting with so recently that the control plane's "online"
// for it may be this machine's own trace rather than another device.
func (m *Manager) Left_recently(client_id string) bool {
	m.left_mu.Lock()
	defer m.left_mu.Unlock()
	return client_id != "" && client_id == m.left_profile && m.now().Sub(m.left_at) < left_profile_trace
}

// Set_profile makes this installation connect with another of the
// account's profiles. A tunnel that is up is rebuilt on the same node and
// protocol with that profile's config; if the config cannot be had, the
// tunnel stays as it was and the choice is still recorded for the next
// connect. The caller checks that the profile is the account's.
func (m *Manager) Set_profile(ctx context.Context, client_id string) (result error) {
	m.cancel_monitor_probe()
	if client_id == "" {
		return &Fault{Kind: Fault_configuration, Op: "set_profile", Err: errors.New("connection: no profile named")}
	}
	m.op_mu.Lock()
	defer m.op_mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	local, err := m.store.Load()
	if err != nil {
		return err
	}
	if local.Client_id == client_id {
		return nil
	}
	rebuilt := false
	defer func() {
		if result == nil && ctx.Err() == nil {
			m.bus.Publish(events.Event{Kind: events.Kind_profile_changed, At: m.now()})
			// The tunnel came back with the other profile's keys: a
			// connection like any other, and the journal says where.
			if rebuilt {
				m.publish(events.Kind_connection_established, nil)
			}
		}
	}()
	left := local.Client_id
	local.Client_id = client_id
	if err := m.store.Save(local); err != nil {
		return err
	}
	m.local_state = local
	if left != "" {
		m.left_mu.Lock()
		m.left_profile, m.left_at = left, m.now()
		m.left_mu.Unlock()
	}

	if state := m.get_state(); state != State_connected && state != State_degraded {
		return nil
	}
	token, err := m.token.Token(ctx)
	if err != nil {
		return &Fault{Kind: Fault_authentication, Op: "set_profile", Err: err}
	}
	config, err := m.control_plane.Fetch_config(ctx, token, client_id, m.current.Protocol, m.current.Node_id, profile_for(effective_mode(m.local_state)))
	if err != nil {
		return classify_control_plane_error("set_profile", err)
	}
	m.current_config = m.connection_config(m.current, config)
	if m.retry_current(ctx) {
		rebuilt = true
		return nil
	}
	m.handle_lost(ctx, m.now())
	if state := m.get_state(); state == State_connected || state == State_degraded {
		return nil
	}
	return &Fault{Kind: Fault_protocol_connection, Op: "set_profile", Err: fmt.Errorf("connection: the tunnel did not come back with profile %s", client_id)}
}
