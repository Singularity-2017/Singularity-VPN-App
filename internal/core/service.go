package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/buildinfo"
	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/journal"
	"singularity-vpn-app/internal/matchserver"
	"singularity-vpn-app/internal/security"
	"singularity-vpn-app/internal/state"
)

// Application is what the pipe drives. The assertion is here rather than in
// a comment so that a change to either side stops the build instead of
// stopping the service.
var _ ipc.Service = (*Application)(nil)

// Status answers without waiting for anything in flight: connection.Manager
// keeps its state behind its own lock precisely so a status query does not
// queue behind a connect that may take thirty seconds.
func (a *Application) Status(ctx context.Context) (ipc.Status_result, error) {
	signed_in, err := a.signed_in()
	if err != nil {
		// A store that cannot be read is not a person who is signed out.
		// Reporting it as one would send somebody to type their password
		// again to fix a locked file.
		return ipc.Status_result{}, map_error(err)
	}

	snapshot := a.manager.Status_snapshot()
	result := ipc.Status_result{
		State:     string(snapshot.State),
		Signed_in: signed_in,
	}
	result.Statistics_error = a.statistics_error.Load()
	if a.statistics != nil {
		stats := a.statistics.Snapshot()
		result.Statistics = &stats
	}
	if result.State == string(connection.State_degraded) {
		detail := snapshot.Degradation
		result.Degraded_latency, result.Degraded_probes = detail.High_latency, detail.Failed_probes
		if detail.Resent_probes {
			resent := detail.Resent * 100
			result.Degraded_resent, result.Resent_percent = true, &resent
		}
		result.Degraded_udp = detail.Udp_lost
		if detail.High_latency && detail.Baseline_rtt > 0 {
			baseline := float64(detail.Baseline_rtt) / float64(time.Millisecond)
			result.Baseline_rtt_ms = &baseline
		}
	}
	if signed_in {
		// Who is signed in, for the account menu. A state that cannot be
		// read leaves the name out rather than failing the status.
		if local, err := a.store.Load(); err == nil {
			result.Username = local.Username
		}
		result.Device_name = a.device_name
	}

	session, ok := snapshot.Session, snapshot.Has_session
	if !ok {
		return result, nil
	}
	result.Node_id = session.Node_id
	result.Protocol = session.Protocol
	result.Tunnel = session.Tunnel
	result.Session_id = session.Id
	if !session.Started_at.IsZero() {
		// Since stays absent unless there is a real moment to report. See
		// the comment on ipc.Status_result for why the field is a pointer.
		started := session.Started_at
		result.Since = &started
	}
	result.Node_name = session.Node_name
	result.Country = session.Country
	result.City = session.City
	result.Reason = session.Reason
	for _, g := range session.Goals {
		result.Goals = append(result.Goals, ipc.Goal_outcome{Goal: g.Goal, Applied: g.Applied, Why: g.Why, Compared: g.Compared})
	}

	// A sample without a round trip -- a prober that confirmed reachability
	// without timing it -- reports nothing rather than a perfect zero.
	// The UDP figures stand on their own: a sample whose dials were all
	// resent has no TCP round trip but still measured UDP.
	if sample, ok := snapshot.Sample, snapshot.Has_sample; ok && sample.Udp_measured {
		udp_loss := sample.Udp_loss * 100
		result.Udp_loss_percent = &udp_loss
		if sample.Udp_rtt > 0 {
			udp_rtt := float64(sample.Udp_rtt) / float64(time.Millisecond)
			result.Udp_rtt_ms = &udp_rtt
		}
	}
	if sample, ok := snapshot.Sample, snapshot.Has_sample; ok && sample.Rtt > 0 {
		rtt := float64(sample.Rtt) / float64(time.Millisecond)
		loss := sample.Packet_loss * 100
		result.Rtt_ms = &rtt
		result.Loss_percent = &loss
		if taken := sample.Taken_at; !taken.IsZero() {
			result.Measured_at = &taken
		}
	}
	return result, nil
}

// signed_in reports whether a device token is stored. A key that was never
// set is a clear no; any other failure of the store is a fault, and the
// caller decides what to do with it. A store that can tell presence is
// asked that, so a status poll does not decrypt the token.
func (a *Application) signed_in() (bool, error) {
	if presence, ok := a.secrets.(security.Presence); ok {
		has, err := presence.Has(security.Key_device_token)
		if err != nil {
			return false, fmt.Errorf("core: reading the device token: %w", err)
		}
		return has, nil
	}
	if _, err := a.secrets.Get(security.Key_device_token); err != nil {
		if errors.Is(err, security.Err_not_found) {
			return false, nil
		}
		return false, fmt.Errorf("core: reading the device token: %w", err)
	}
	return true, nil
}

// Connect asks the state machine to bring a tunnel up.
func (a *Application) Connect(ctx context.Context, params ipc.Connect_params) error {
	if err := params.Validate(); err != nil {
		return err
	}

	mode, err := connection_mode(params.Mode)
	if err != nil {
		return err
	}

	a.logger.Info("connect requested", "mode", params.Mode, "node_id", params.Node_id, "protocol", params.Protocol)
	// Recorded before the attempt, not after its success: what the person
	// asked for is to be connected, and a connect that failed because the
	// network was down is exactly the one worth repeating at the next start.
	// Not for someone who is not signed in: their connect fails at once,
	// and a wish recorded now would connect them at a reboot after they
	// sign in, without their ever having connected.
	if signed_in, err := a.signed_in(); err == nil && signed_in {
		if err := a.manager.Set_connect_on_start(true); err != nil {
			a.logger.Warn("could not record that a connection is wanted", "err", err)
		}
	}
	return map_error(a.manager.Connect(ctx, connection.Connect_request{
		Mode:     mode,
		Node_id:  params.Node_id,
		Protocol: params.Protocol,
	}))
}

// connection_mode turns the wire's mode string into connection.Mode.
//
// It refuses anything it does not recognise instead of defaulting, and that
// is the whole reason it is a function rather than three lines inside
// Connect: connection.Mode's zero value is Mode_auto, so any path that
// forgets to fail here connects a person somewhere nobody asked for. Params
// that reached Connect through the pipe were validated already, which makes
// the refusal unreachable from there -- and unreachable is exactly why it
// needs a test of its own.
func connection_mode(mode string) (connection.Mode, error) {
	switch mode {
	case ipc.Mode_auto:
		return connection.Mode_auto, nil
	case ipc.Mode_manual:
		return connection.Mode_manual, nil
	default:
		return 0, fmt.Errorf("core: connect: unknown mode %q", mode)
	}
}

// Disconnect takes the tunnel down.
func (a *Application) Disconnect(ctx context.Context) error {
	a.logger.Info("disconnect requested")
	// A person's disconnect is the one way to say "stay off". The service
	// stopping for a shutdown goes through Stop, which leaves this alone.
	// The tunnel comes down regardless; but a "no" that was not recorded
	// means the next start connects a person who asked to stay off, so it
	// is reported rather than only logged.
	wish_err := a.manager.Set_connect_on_start(false)
	if err := a.manager.Disconnect(ctx); err != nil {
		return map_error(err)
	}
	if wish_err != nil {
		return fmt.Errorf("core: disconnected, but could not record that no connection is wanted, so the next start may connect again: %w", wish_err)
	}
	return nil
}

// Sign_in exchanges a username and a password for a device token and stores
// it. The password arrives over the pipe, is used for exactly one call, and
// is never written anywhere: not to the store, not to the state file, not
// to a log line. The only thing logged here is the username.
//
// The session token from Login is equally short-lived. It exists to mint the
// device token and is then dropped: it lives an hour, and a token the
// service would have to renew hourly is the problem the device endpoint was
// added to solve (design section 5).
func (a *Application) Sign_in(ctx context.Context, params ipc.Sign_in_params) (ipc.Sign_in_result, error) {
	if err := params.Validate(); err != nil {
		return ipc.Sign_in_result{}, err
	}
	a.logger.Info("sign-in requested", "username", params.Username)

	session_token, err := a.control_plane.Login(ctx, params.Username, params.Password)
	if err != nil {
		if api.Is_code(err, api.Code_unauthorized) {
			return ipc.Sign_in_result{}, fmt.Errorf("%w: %w", ipc.Err_unauthorized, err)
		}
		return ipc.Sign_in_result{}, fmt.Errorf("core: sign in: %w", err)
	}

	// Anything the control plane refuses here keeps its own message all
	// the way to the person -- api.Code_device_token_cannot_mint above all,
	// which is a real answer about their account and not an internal
	// failure they can do nothing about.
	device, err := a.control_plane.Mint_device_token(ctx, session_token, a.device_name)
	if err != nil {
		return ipc.Sign_in_result{}, fmt.Errorf("core: minting a device token: %w", err)
	}

	// The token first, because it is the credential everything else rests
	// on, and both writes are checked. A half-written pair is worse than
	// no pair at all: it looks signed in from one angle and signed out
	// from the other.
	if err := a.secrets.Set(security.Key_device_token, []byte(device.Device_token)); err != nil {
		return ipc.Sign_in_result{}, fmt.Errorf("core: storing the device token: %w", err)
	}
	if err := a.secrets.Set(security.Key_device_id, []byte(device.Device_id)); err != nil {
		return ipc.Sign_in_result{}, fmt.Errorf("core: storing the device id: %w", err)
	}

	local, err := a.store.Load()
	if err != nil {
		return ipc.Sign_in_result{}, fmt.Errorf("core: loading the local state: %w", err)
	}
	local.Device_id = device.Device_id
	local.Username = params.Username
	if err := a.store.Save(local); err != nil {
		return ipc.Sign_in_result{}, fmt.Errorf("core: saving the local state: %w", err)
	}

	a.logger.Info("signed in", "username", params.Username, "device_id", device.Device_id)
	return ipc.Sign_in_result{Device_id: device.Device_id}, nil
}

// Sign_out removes the stored credentials, after taking the tunnel down.
//
// Client_id is deliberately left alone. Clearing it orphans a profile on the
// control plane that still counts against the account's client_limit, and
// the person cannot get that slot back. The case that makes clearing it look
// necessary -- signing in as a different account on the same machine -- is
// already handled a level down: the control plane answers CLIENT_NOT_FOUND
// for a client_id belonging to somebody else, and
// connection.Manager.provision re-provisions exactly once when it sees that.
func (a *Application) Sign_out(ctx context.Context) error {
	a.logger.Info("sign-out requested")

	if err := a.manager.Set_connect_on_start(false); err != nil {
		// Refuse before anything is removed: signed out with the wish
		// still recorded would connect the next person who signs in here
		// at the next start.
		return map_error(fmt.Errorf("core: sign-out: recording that no connection is wanted: %w", err))
	}
	if err := a.manager.Disconnect(ctx); err != nil {
		if errors.Is(err, connection.Err_busy) {
			// Something else is mid-operation, so the tunnel is still
			// there. Removing the credentials out from under it would
			// leave a machine connected with nothing left to reconnect
			// it. Refuse and let the caller ask again.
			return map_error(err)
		}
		// Any other failure still left the state machine disconnected --
		// Disconnect clears the session and reports the problem rather
		// than holding on to a connection somebody asked to end. Keeping
		// the credentials because the teardown complained would be the
		// worse outcome, so this is logged and the sign-out continues.
		a.logger.Warn("disconnect during sign-out reported a problem", "error", err.Error())
	}

	// A server picked by hand is one this account may use; the next
	// account to sign in here may not, and would be refused on every
	// connect. The goals themselves are this machine's and stay.
	if strategy, err := a.manager.Strategy(); err == nil && strategy.Node_id != "" {
		strategy.Node_id = ""
		if err := a.manager.Set_strategy(strategy); err != nil {
			a.logger.Warn("could not forget the server picked by hand", "err", err)
		}
	}

	// The servers this account was listed are not the next account's to
	// measure, nor anybody's once nobody is signed in.
	a.hosts_mu.Lock()
	a.hosts = nil
	a.hosts_mu.Unlock()

	if err := a.secrets.Delete(security.Key_device_token); err != nil {
		return fmt.Errorf("core: removing the device token: %w", err)
	}
	if err := a.secrets.Delete(security.Key_device_id); err != nil {
		return fmt.Errorf("core: removing the device id: %w", err)
	}

	local, err := a.store.Load()
	if err != nil {
		return fmt.Errorf("core: loading the local state: %w", err)
	}
	local.Device_id = ""
	local.Username = ""
	if err := a.store.Save(local); err != nil {
		return fmt.Errorf("core: saving the local state: %w", err)
	}
	return nil
}

// Subscribe hands out the event stream.
func (a *Application) Subscribe() (<-chan events.Event, func()) {
	return a.bus.Subscribe()
}

// Version reports only what the build actually supplied. A field the build
// left empty stays empty here and is omitted on the wire -- never
// "unknown", never "dev".
func (a *Application) Version(ctx context.Context) (ipc.Version_result, error) {
	return ipc.Version_result{
		Version: buildinfo.Version,
		Commit:  buildinfo.Commit,
		Built:   buildinfo.Built_at,
	}, nil
}

// Strategy answers Command_strategy: what the connection is for, as last
// recorded.
func (a *Application) Strategy(ctx context.Context) (ipc.Strategy, error) {
	s, err := a.manager.Strategy()
	if err != nil {
		return ipc.Strategy{}, map_error(err)
	}
	return ipc.Strategy{Anonymity: s.Anonymity, Bypass: s.Bypass, Country: s.Country, Route: s.Route, Node_id: s.Node_id}, nil
}

// Set_strategy answers Command_set_strategy: it records the strategy and,
// if a connection is up, moves it to wherever the control plane now points.
// A strategy that was recorded but could not be applied is reported as the
// failure to apply it; the record stands, and the next connect uses it.
func (a *Application) Set_strategy(ctx context.Context, strategy ipc.Strategy) error {
	if err := strategy.Validate(); err != nil {
		return err
	}
	a.logger.Info("strategy set", "anonymity", strategy.Anonymity, "bypass", strategy.Bypass,
		"country", strategy.Country, "route", strategy.Route, "node_id", strategy.Node_id)
	if err := a.manager.Set_strategy(state.Strategy{
		Anonymity: strategy.Anonymity,
		Bypass:    strategy.Bypass,
		Country:   strategy.Country,
		Route:     strategy.Route,
		Node_id:   strategy.Node_id,
	}); err != nil {
		return map_error(err)
	}
	if err := a.manager.Apply_strategy(ctx); err != nil {
		return map_error(err)
	}
	// A goal about the whole machine -- anonymity, getting past blocks --
	// moves a games-only tunnel to carrying everything, and turning it off
	// moves it back. Apply_strategy only changes the node.
	return map_error(a.manager.Apply_games(ctx))
}

// Games answers Command_games: the games carried through the tunnel, as
// last recorded.
func (a *Application) Games(ctx context.Context) (ipc.Games, error) {
	ids, region, err := a.manager.Games()
	if err != nil {
		return ipc.Games{}, map_error(err)
	}
	return ipc.Games{Games: append([]string{}, ids...), Region: region, Pings: a.game_pings.list(ids), In_match: a.manager.In_match(), Match_servers: a.match_servers(ids)}, nil
}

// match_servers is, for each of ids, the server waiting for the player to
// confirm and the tally so far, for the games that have either.
func (a *Application) match_servers(ids []string) []ipc.Match_server {
	var out []ipc.Match_server
	for _, id := range ids {
		m := ipc.Match_server{Game: id}
		tally := a.matches.Tally(id)
		m.Right, m.Confirmed = tally.Right, tally.Confirmed
		c, offered := a.matches.Offer(id)
		if offered {
			m.Server, m.Packets, m.Runner_up, m.Total = c.Server.String(), c.Packets, c.Runner_up, c.Total
		}
		if offered || m.Confirmed > 0 {
			out = append(out, m)
		}
	}
	return out
}

// Answer_match_server answers Command_answer_match_server: it records the
// player's word on the server offered for a game. An offer that has moved
// on meanwhile is refused, so an answer is never put to a server the
// player was not shown.
func (a *Application) Answer_match_server(ctx context.Context, answer ipc.Match_server_answer) error {
	server, err := netip.ParseAddrPort(answer.Server)
	if err != nil {
		return err
	}
	shown, err := matchserver.Parse_shown(answer.Shown)
	if err != nil {
		return err
	}
	c, err := a.matches.Answer(answer.Game, server, matchserver.Answer(answer.Answer), shown, a.now())
	if errors.Is(err, matchserver.Error_offer_changed) {
		return fmt.Errorf("%w: %w", ipc.Err_offer_changed, err)
	}
	if err != nil {
		a.logger.Warn("recording a match server answer failed", "error", err.Error())
		return err
	}
	a.logger.Info("match server answered", "game", c.Game, "detected", c.Detected.String(), "verdict", string(c.Verdict))
	return nil
}

// Set_games answers Command_set_games: it records the games and, if a
// tunnel is up and what it carries has changed, rebuilds it on the same
// node. As with a strategy, a choice recorded but not applied is reported
// as the failure to apply it; the record stands.
func (a *Application) Set_games(ctx context.Context, games ipc.Games) error {
	if err := games.Validate(); err != nil {
		return err
	}
	a.logger.Info("games set", "games", games.Games, "region", games.Region)
	_, was, _ := a.manager.Games()
	if err := a.manager.Set_games(games.Games, games.Region); err != nil {
		return map_error(err)
	}
	if err := a.manager.Apply_games(ctx); err != nil {
		return map_error(err)
	}
	// Another region is another place to be near: an automatic choice is
	// asked for again, and moves only if the answer is somewhere else.
	if games.Region != was {
		return map_error(a.manager.Apply_strategy(ctx))
	}
	return nil
}

// Journal answers Command_journal: the operations the service has logged
// since it started, as many as the journal keeps.
func (a *Application) Journal(ctx context.Context) (ipc.Journal_result, error) {
	result := ipc.Journal_result{Entries: []journal.Entry{}}
	if a.journal != nil {
		result.Entries = a.journal.List()
	}
	return result, nil
}

// Tunnel_mode answers Command_tunnel_mode: the profile the person chose,
// always "vpn" or "antizapret".
func (a *Application) Tunnel_mode(ctx context.Context) (ipc.Tunnel_mode, error) {
	mode, err := a.manager.Tunnel_mode()
	if err != nil {
		return ipc.Tunnel_mode{}, map_error(err)
	}
	return ipc.Tunnel_mode{Mode: mode}, nil
}

// Set_tunnel_mode answers Command_set_tunnel_mode: it records the choice
// and, if a tunnel is up and was built with the other profile, rebuilds it
// on the same node. A choice recorded but not applied is reported as the
// failure to apply it; the record stands.
func (a *Application) Set_tunnel_mode(ctx context.Context, mode ipc.Tunnel_mode) error {
	if err := mode.Validate(); err != nil {
		return err
	}
	a.logger.Info("tunnel mode set", "mode", mode.Mode)
	if err := a.manager.Set_tunnel_mode(mode.Mode); err != nil {
		return map_error(err)
	}
	return map_error(a.manager.Apply_tunnel(ctx))
}

// Profiles answers Command_profiles: the account's profiles, and which one
// this installation connects with.
func (a *Application) Profiles(ctx context.Context) (ipc.Profiles_result, error) {
	list, err := a.account_profiles(ctx)
	if err != nil {
		return ipc.Profiles_result{}, err
	}
	current, err := a.manager.Client_id()
	if err != nil {
		return ipc.Profiles_result{}, map_error(err)
	}
	result := ipc.Profiles_result{Profiles: make([]ipc.Profile, 0, len(list))}
	for _, p := range list {
		online := p.Online
		// The profile this machine just left still reads as connected for
		// a few minutes; that is not another device, so it is not known.
		if online != nil && *online && a.manager.Left_recently(p.Id) {
			online = nil
		}
		result.Profiles = append(result.Profiles, ipc.Profile{Id: p.Id, Label: p.Label, Online: online, Current: p.Id == current})
	}
	return result, nil
}

// Set_profile answers Command_set_profile: only one of the account's own
// profiles is taken, and a tunnel that is up is rebuilt with it.
func (a *Application) Set_profile(ctx context.Context, profile ipc.Set_profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	list, err := a.account_profiles(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(list, func(p api.Account_profile) bool { return p.Id == profile.Id }) {
		return fmt.Errorf("core: profile %s is not this account's", profile.Id)
	}
	return map_error(a.manager.Set_profile(ctx, profile.Id))
}

func (a *Application) account_profiles(ctx context.Context) ([]api.Account_profile, error) {
	book, ok := a.control_plane.(connection.Profile_book)
	if !ok {
		return nil, errors.New("core: this control plane does not list profiles")
	}
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return nil, map_error(err)
	}
	list, err := book.Profiles(ctx, token)
	if err != nil {
		return nil, map_error(err)
	}
	return list, nil
}

// Servers answers Command_servers: the servers this account may be put on,
// as the control plane lists them. It also remembers which of them have an
// address to measure, for Ping_servers; the address itself is not part of
// the answer.
func (a *Application) Servers(ctx context.Context) (ipc.Servers_result, error) {
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return ipc.Servers_result{}, map_error(err)
	}
	list, err := a.control_plane.Servers(ctx, token)
	if err != nil {
		return ipc.Servers_result{}, map_error(err)
	}
	result := ipc.Servers_result{Servers: make([]ipc.Server, 0, len(list))}
	hosts := make(map[string]string, len(list))
	for _, s := range list {
		result.Servers = append(result.Servers, ipc.Server{
			Node_id: s.Node_id, Name: s.Name, Country: s.Country, Region: s.Region, City: s.City, Available: s.Available,
		})
		if s.Host != "" {
			hosts[s.Node_id] = s.Host
		}
	}

	// Replaced, not merged: a server the control plane no longer lists, or
	// one whose address changed, must stop being measured at the old one.
	a.hosts_mu.Lock()
	a.hosts = hosts
	a.hosts_mu.Unlock()
	return result, nil
}
