package connection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/evidence"
	"singularity-vpn-app/internal/games"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/recovery"
	"singularity-vpn-app/internal/state"
)

// probe_errors_before_lost is how many consecutive failed probes it takes
// before an unknown is treated as Verdict_lost: one error is noise a flaky
// probe can produce on its own; two in a row is a symptom worth acting on.
const probe_errors_before_lost = 2

// Control_plane is the subset of api.Client this package drives. It is an
// interface, not *api.Client, so a test scripts a control plane without a
// network.
type Control_plane interface {
	Recommend(ctx context.Context, token string, request api.Recommend_request) (api.Recommendation, error)
	Create_profile(ctx context.Context, token, protocol, node_id string, duration_days int) (api.Profile, error)
	Fetch_config(ctx context.Context, token, client_id, protocol, node_id, mode string) (api.Vpn_config, error)
}

// Token_source hands over the token the service holds. The manager never
// sees a password and never stores the token: stage 4 wires this to the
// secure store, and until then a test fake is the only implementation.
type Token_source interface {
	Token(ctx context.Context) (string, error)
}

// State_store is what this package needs from stage 1's state.Store:
// enough to read and write Client_id and the last-used node and protocol.
// *state.Store satisfies this without any adapter.
type State_store interface {
	Load() (state.State, error)
	Save(s state.State) error
}

// Dependencies is everything the manager needs and nothing it invents,
// except Now and New_session_id: those two may default, because a real
// clock and a real random id are the only pieces of this package that are
// reasonable to assume in production and safe to fake in a test.
type Dependencies struct {
	// Observers must be fast, memory-only and must not call back into Manager.
	Event_observer  func(events.Event)
	Sample_observer func(diagnostics.Sample)
	Evidence        *evidence.Collector
	Control_plane   Control_plane
	Token           Token_source
	Protocols       protocols.Manager
	Prober          diagnostics.Prober
	Policy          *recovery.Policy
	Store           State_store
	Bus             *events.Bus
	Now             func() time.Time
	New_session_id  func() string
	Config          Config

	// Logger receives the outcome of a network change. nil discards.
	Logger *slog.Logger
}

// Mode is whether a connect was asked for by the person (with a specific
// node and protocol) or left to the control plane's recommendation.
type Mode int

const (
	Mode_auto Mode = iota
	Mode_manual
)

// Connect_request is what Connect is asked to do. Node_id and Protocol are
// only read in Mode_manual; in Mode_auto the control plane chooses.
type Connect_request struct {
	Mode     Mode
	Node_id  string
	Protocol string
}

// Err_busy is what Connect, Disconnect or Switch return when the current
// state genuinely forbids the operation -- mid-switch, mid-disconnect, or
// already busy some other way. It exists for that refusal, not for the
// operation mutex someone else happens to be holding: op_mu simply makes a
// concurrent caller wait, which is correct, because there is nothing yet to
// act on.
var Err_busy = errors.New("connection: busy with another operation")

// Manager is the state machine: it decides when to provision a profile,
// when to bring a protocol up, what a tick's probe means, and when recovery
// should try another target. It contains no platform code of its own --
// everything impure above is an interface, and every timer is a parameter,
// not a clock read from inside.
type Manager struct {
	event_observer       func(events.Event)
	sample_observer      func(diagnostics.Sample)
	evidence_loss_reason string
	evidence_sleep_seen  bool
	evidence             *evidence.Collector
	evidence_session_id  string
	evidence_started     time.Time
	evidence_target      recovery.Target
	evidence_mode        string
	evidence_open        bool
	evidence_last_tick   time.Time
	control_plane        Control_plane
	token                Token_source
	protocols            protocols.Manager
	prober               diagnostics.Prober
	policy               *recovery.Policy
	store                State_store
	bus                  *events.Bus
	now                  func() time.Time
	new_session_id       func() string
	config               Config
	logger               *slog.Logger

	// op_mu serializes Connect, Disconnect, Switch and Tick: they mutate
	// one session and must never interleave. A Tick that arrives while a
	// connect is running simply waits, which is correct -- there is
	// nothing to monitor yet.
	op_mu sync.Mutex

	probe_mu     sync.Mutex
	probe_cancel context.CancelFunc
	monitor      probe_schedule // guarded by op_mu

	// abandon cancels the connect holding op_mu, if one is. A person's
	// disconnect must not queue behind a connect that is waiting out a
	// control plane that does not answer; it has its own lock because the
	// whole point is to reach it while op_mu is held.
	abandon_mu sync.Mutex
	abandon    context.CancelFunc

	// left_mu guards the profile this installation last stopped
	// connecting with and when; see Left_recently.
	left_mu      sync.Mutex
	left_profile string
	left_at      time.Time

	// state_mu guards only state and session, so State() and Session() can
	// answer a status query without waiting for a Connect that might take
	// up to Connect_timeout to finish.
	state_mu    sync.RWMutex
	state       State
	transitions uint64    // how many times state has changed; see Transitions
	changed_at  time.Time // when state last changed; see Changed_at
	session     Session
	has_session bool

	// sample is the last successful probe of the current session, for a
	// status query; has_sample is false until one exists, so nothing is
	// ever reported for a tunnel that has not been measured yet.
	sample      diagnostics.Sample
	has_sample  bool
	degradation diagnostics.Degradation

	// The rest is only ever touched under op_mu.
	mode            Mode
	current         recovery.Target
	current_config  protocols.Connection_config
	protocol_handle protocols.Protocol
	alternatives    []recovery.Target

	// recommended is the control plane's last recommendation, kept so a
	// session on the node it named can carry that node's description.
	recommended     api.Recommendation
	has_recommended bool
	local_state     state.State

	baseline           diagnostics.Baseline
	degraded_since     time.Time
	degraded_since_set bool
	// degraded_pending is set by one bad sample on a connected tunnel and
	// cleared by the next sample: a second bad one confirms degradation.
	degraded_pending         bool
	consecutive_probe_errors int

	// retried_at is when a lost tunnel last came back on a retry of the
	// same target. Lost again soon after, the target is not retried a
	// second time: a path that lets the handshake through and drops the
	// tunnel seconds later -- plain WireGuard under DPI -- would otherwise
	// be rebuilt forever.
	retried_at     time.Time
	retried_target recovery.Target

	// companion is the AntiZapret tunnel kept beside a games-only one.
	companion         companion
	last_reevaluation time.Time
	// held_in_match is set once a degraded connection has been kept
	// because of a match, so the hold is logged once per episode.
	held_in_match bool

	// in_match is whether a game is in a match, as the service last saw
	// it. While it is, nothing moves a working connection on its own --
	// leaving the node would end the match (owner, 2026-10-17). A dead
	// node is still left: the match is lost either way.
	in_match atomic.Bool
}

// Set_in_match records whether a game is in a match.
func (m *Manager) Set_in_match(on bool) {
	m.in_match.Store(on)
}

// In_match is whether a game is in a match, as last recorded.
func (m *Manager) In_match() bool {
	return m.in_match.Load()
}

// New_manager builds a Manager, refusing a Dependencies missing anything it
// cannot invent. Now and New_session_id are the two it may default.
func New_manager(dependencies Dependencies) (*Manager, error) {
	switch {
	case dependencies.Control_plane == nil:
		return nil, fmt.Errorf("connection: Dependencies.Control_plane is required")
	case dependencies.Token == nil:
		return nil, fmt.Errorf("connection: Dependencies.Token is required")
	case dependencies.Protocols == nil:
		return nil, fmt.Errorf("connection: Dependencies.Protocols is required")
	case dependencies.Prober == nil:
		return nil, fmt.Errorf("connection: Dependencies.Prober is required")
	case dependencies.Policy == nil:
		return nil, fmt.Errorf("connection: Dependencies.Policy is required")
	case dependencies.Store == nil:
		return nil, fmt.Errorf("connection: Dependencies.Store is required")
	case dependencies.Bus == nil:
		return nil, fmt.Errorf("connection: Dependencies.Bus is required")
	}

	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.New_session_id == nil {
		dependencies.New_session_id = default_session_id
	}

	if dependencies.Logger == nil {
		dependencies.Logger = slog.New(slog.DiscardHandler)
	}

	return &Manager{
		event_observer:  dependencies.Event_observer,
		sample_observer: dependencies.Sample_observer,
		logger:          dependencies.Logger,
		evidence:        dependencies.Evidence,
		control_plane:   dependencies.Control_plane,
		token:           dependencies.Token,
		protocols:       dependencies.Protocols,
		prober:          dependencies.Prober,
		policy:          dependencies.Policy,
		store:           dependencies.Store,
		bus:             dependencies.Bus,
		now:             dependencies.Now,
		new_session_id:  dependencies.New_session_id,
		config:          dependencies.Config.with_defaults(),
		state:           State_disconnected,
	}, nil
}

// State reports the current state without waiting for any in-flight
// Connect, Disconnect or Switch to finish.
func (m *Manager) State() State {
	m.state_mu.RLock()
	defer m.state_mu.RUnlock()
	return m.state
}

// Transitions counts every change of state since the manager was built. A
// caller that must know whether the connection moved while it worked reads
// it before and after: equal means no state change happened in between,
// even one that went there and back.
func (m *Manager) Transitions() uint64 {
	m.state_mu.RLock()
	defer m.state_mu.RUnlock()
	return m.transitions
}

// Changed_at is when the state last changed; zero before the first change.
func (m *Manager) Changed_at() time.Time {
	m.state_mu.RLock()
	defer m.state_mu.RUnlock()
	return m.changed_at
}

// Session reports the current session, if any, the same way State does.
func (m *Manager) Session() (Session, bool) {
	m.state_mu.RLock()
	defer m.state_mu.RUnlock()
	return m.session, m.has_session
}

func (m *Manager) get_state() State {
	m.state_mu.RLock()
	defer m.state_mu.RUnlock()
	return m.state
}

// Last_sample reports the last successful probe of the current session.
// A new session starts with none.
func (m *Manager) Last_sample() (diagnostics.Sample, bool) {
	m.state_mu.RLock()
	defer m.state_mu.RUnlock()
	return m.sample, m.has_sample
}

func (m *Manager) Degradation() diagnostics.Degradation {
	m.state_mu.RLock()
	defer m.state_mu.RUnlock()
	return m.degradation
}

// Status_snapshot keeps a diagnosis with the sample and session it describes.
// Reading them separately could combine two different monitoring passes.
type Status_snapshot struct {
	State       State
	Session     Session
	Has_session bool
	Sample      diagnostics.Sample
	Has_sample  bool
	Degradation diagnostics.Degradation
}

func (m *Manager) Status_snapshot() Status_snapshot {
	m.state_mu.RLock()
	defer m.state_mu.RUnlock()
	return Status_snapshot{State: m.state, Session: m.session, Has_session: m.has_session, Sample: m.sample, Has_sample: m.has_sample, Degradation: m.degradation}
}

func (m *Manager) set_session(session Session, ok bool) {
	m.state_mu.Lock()
	m.session = session
	m.has_session = ok
	m.sample = diagnostics.Sample{}
	m.has_sample = false
	m.degradation = diagnostics.Degradation{}
	m.state_mu.Unlock()
}

func (m *Manager) set_sample(sample diagnostics.Sample, detail diagnostics.Degradation) {
	m.state_mu.Lock()
	m.sample = sample
	m.has_sample = true
	m.degradation = detail
	m.state_mu.Unlock()
	if m.sample_observer != nil {
		m.sample_observer(sample)
	}
}

// set_state moves the machine to to, publishing Kind_state_changed. A
// transition Allowed forbids is refused rather than performed: reaching
// here with a bad transition is a programming mistake in this package, and
// this code runs inside a service that owns a person's network route, where
// a panic is worse than a wrong-but-stable state. The refusal keeps the
// state it had and reports the mistake as an ordinary event instead of
// vanishing silently.
func (m *Manager) set_state(to State) {
	m.state_mu.Lock()
	from := m.state
	if !Allowed(from, to) {
		m.state_mu.Unlock()
		m.publish(events.Kind_error, fmt.Errorf("connection: refused invalid transition %s -> %s", from, to))
		return
	}
	if from == to {
		m.state_mu.Unlock()
		return // a no-op self-transition changes nothing and emits nothing
	}
	m.state = to
	m.transitions++
	m.changed_at = m.now()
	m.state_mu.Unlock()
	m.publish(events.Kind_state_changed, State_change{From: from, To: to})
}

func (m *Manager) publish(kind events.Kind, data any) {
	session_id := ""
	if session, ok := m.Session(); ok {
		session_id = session.Id
	}
	event := events.Event{Kind: kind, At: m.now(), Session_id: session_id, Data: data}
	if m.event_observer != nil {
		m.event_observer(event)
	}
	m.bus.Publish(event)
}

// --- Connect -------------------------------------------------------------

// Connect brings up a tunnel, choosing the target automatically or taking
// the one the person picked, provisioning a profile if this installation
// has none yet. CLIENT_CORE.md section 43, CLIENT_AUTO_SWITCH.md section 3.
func (m *Manager) Connect(ctx context.Context, request Connect_request) error {
	m.prepare_evidence(ctx)
	m.op_mu.Lock()
	defer m.op_mu.Unlock()
	if err := m.connect_locked(ctx, request); err != nil {
		return err
	}
	m.drop_unwanted_companion(ctx)
	return nil
}

// Connect_if_wanted connects only if Set_connect_on_start last recorded a
// yes and nothing has a connection in hand or in flight, deciding and
// acting under one hold of op_mu. Checked outside the lock, a disconnect
// pressed between the check and the connect would be overridden: it finds
// nothing to disconnect, records its no, and the connect goes ahead anyway.
// It reports whether it attempted anything.
func (m *Manager) Connect_if_wanted(ctx context.Context, request Connect_request) (bool, error) {
	m.prepare_evidence(ctx)
	m.op_mu.Lock()
	defer m.op_mu.Unlock()

	local, err := m.store.Load()
	if err != nil {
		return false, err
	}
	if !local.Connect_on_start {
		return false, nil
	}
	if current := m.get_state(); current != State_disconnected && current != State_failed {
		return false, nil
	}
	return true, m.connect_locked(ctx, request)
}

func (m *Manager) connect_locked(ctx context.Context, request Connect_request) error {
	current := m.get_state()
	if current == State_connected {
		return nil // CLIENT_CORE.md 35: a second Connect does not make a second tunnel
	}
	if !Allowed(current, State_connecting) {
		return Err_busy
	}

	connect_ctx := ctx
	var cancel context.CancelFunc
	if m.config.Connect_timeout > 0 {
		connect_ctx, cancel = context.WithTimeout(ctx, m.config.Connect_timeout)
		defer cancel()
	}
	connect_ctx, abandon := context.WithCancel(connect_ctx)
	m.set_abandon(abandon)
	defer func() {
		m.set_abandon(nil)
		abandon()
	}()

	now := m.now()
	m.mode = request.Mode
	m.policy.Reset() // a person asked for this connect: the budget and cooldowns start over
	m.set_state(State_connecting)
	m.publish(events.Kind_connection_started, nil)
	// Only a recommendation asked for during this connect may describe
	// the session it produces; one left over from an earlier connect says
	// how some other choice was made.
	m.has_recommended = false

	token, err := m.token.Token(connect_ctx)
	if err != nil {
		return m.fail(now, &Fault{Kind: Fault_authentication, Op: "connect", Err: err})
	}

	local, err := m.store.Load()
	if err != nil {
		return m.fail(now, &Fault{Kind: Fault_configuration, Op: "connect", Err: err})
	}
	m.local_state = local

	target, config, fault := m.provision(connect_ctx, token, request)
	if fault != nil {
		return m.fail(now, fault)
	}

	return m.connect_loop(connect_ctx, now, token, target, config)
}

// connect_loop tries to establish target, and on a hard failure (auto mode
// only) keeps asking recovery.Policy for another target until one works or
// the policy refuses. CLIENT_AUTO_SWITCH.md section 3: "a failure at step 6
// or 7 is a hard failure ... ask the policy for another target and switch
// to it."
func (m *Manager) connect_loop(ctx context.Context, now time.Time, token string, target recovery.Target, config api.Vpn_config) error {
	for {
		protocol, fault := m.establish(ctx, target, config)
		if fault == nil {
			return m.settle_connected(ctx, now, target, protocol, config, "connect", events.Kind_connection_established, nil)
		}

		m.policy.Record_failure(now, target)
		if m.mode != Mode_auto {
			return m.fail(now, fault)
		}

		next, err := m.policy.Next_target(now, recovery.Reason_hard_fail, target, m.alternatives)
		if err != nil {
			// Err_budget_spent or Err_no_target: nothing left to try. The
			// fault reported is the technical reason the last attempt
			// failed, which is what the person needs to see.
			return m.fail(now, fault)
		}

		next_config, cerr := m.control_plane.Fetch_config(ctx, token, m.local_state.Client_id, next.Protocol, next.Node_id, profile_for(effective_mode(m.local_state)))
		if cerr != nil {
			return m.fail(now, classify_control_plane_error("connect", cerr))
		}
		target, config = next, next_config
	}
}

// provision decides which node and protocol to use and makes sure a
// client_id exists on the control plane for it, provisioning at most once.
// The client_id is saved to the store the moment Create_profile answers --
// before anything else in this connect can fail -- because it is what the
// account's client_limit counts, and a profile provisioned and not recorded
// is a slot the person has paid for and cannot reclaim.
func (m *Manager) provision(ctx context.Context, token string, request Connect_request) (recovery.Target, api.Vpn_config, *Fault) {
	if m.local_state.Client_id == "" {
		return m.create_profile(ctx, token, request)
	}

	target, fault := m.choose_target(ctx, token, request)
	if fault != nil {
		return recovery.Target{}, api.Vpn_config{}, fault
	}

	config, err := m.control_plane.Fetch_config(ctx, token, m.local_state.Client_id, target.Protocol, target.Node_id, profile_for(effective_mode(m.local_state)))
	if err == nil {
		return target, config, nil
	}

	if !api.Is_code(err, api.Code_client_not_found) {
		return recovery.Target{}, api.Vpn_config{}, classify_control_plane_error("fetch_config", err)
	}

	// The stored client_id no longer exists on the control plane. Provision
	// again, exactly once -- looping here would spend the account's
	// client_limit on every retry.
	m.local_state.Client_id = ""
	if serr := m.store.Save(m.local_state); serr != nil {
		return recovery.Target{}, api.Vpn_config{}, &Fault{Kind: Fault_configuration, Op: "connect", Err: serr}
	}
	return m.create_profile(ctx, token, request)
}

// choose_target picks the node and protocol to fetch a config for, when a
// client_id already exists. Manual mode uses what the person chose; auto
// mode asks the control plane and remembers its alternatives for recovery.
func (m *Manager) choose_target(ctx context.Context, token string, request Connect_request) (recovery.Target, *Fault) {
	if request.Mode == Mode_manual {
		return recovery.Target{Node_id: request.Node_id, Protocol: request.Protocol}, nil
	}
	recommendation, err := m.control_plane.Recommend(ctx, token, m.recommend_request())
	if err != nil {
		return recovery.Target{}, classify_control_plane_error("recommend", err)
	}
	target, fault := m.spoken_target(recommendation)
	if fault != nil {
		return recovery.Target{}, fault
	}
	m.alternatives = m.recovery_offers(recommendation, target)
	if m.evidence != nil {
		m.evidence.Remember(ctx, recommendation.Evidence)
	}
	m.recommended = recommendation
	m.has_recommended = true
	return target, nil
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

	// A control plane that ignores the server picked by hand answers for
	// another one; none of its offers is on the picked server, and saying
	// "speaks none of them" would blame the protocols for it.
	if pinned := m.local_state.Strategy.Node_id; pinned != "" && recommendation.Node_id != pinned {
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
	if pinned := m.local_state.Strategy.Node_id; pinned != "" {
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
	if kept := without_reported(eligible, recommendation.Evidence, Evidence_mode(effective_mode(m.local_state))); slices.ContainsFunc(kept, usable) {
		if len(kept) < len(eligible) {
			m.logger.Info("leaving aside what this network reported problems with", "kept", len(kept), "offered", len(eligible))
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

// without_reported drops the targets the evidence for this network carries
// a problem report about, from the app's own observations in the same
// mode: a report made while only games went through says nothing about
// the whole machine going through.
func without_reported(targets []recovery.Target, evidence []api.Connection_evidence, mode string) []recovery.Target {
	reported := map[recovery.Target]bool{}
	for _, row := range evidence {
		if row.Source == "app" && row.Reported > 0 && row.Mode == mode {
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

// games_target is, among offered, a target on node whose protocol can
// carry the games alone: the protocol that last carried traffic on the
// node first, then the offers in order. False when there is none.
// retry_flap_window is how soon after a retry a second loss of the same
// target counts as that target not holding.
const retry_flap_window = time.Minute

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

// state_frozen is the control plane's reach verdict for a protocol that
// connects from this network and then stops passing traffic.
const state_frozen = "frozen"

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
		Anonymity: s.Anonymity,
		Bypass:    s.Bypass,
		Country:   s.Country,
		Route:     s.Route,
		Client_id: m.local_state.Client_id,
		Node_id:   s.Node_id,
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

// create_profile provisions a new client_id. Manual mode passes the
// person's chosen node and protocol through to Create_profile; auto mode
// asks the recommendation first and provisions with its node and a
// protocol this build speaks. Leaving both empty for the server to pick
// was the earlier rule, and it broke where the server's pick was a
// protocol this build has no adapter for: provisioning does not know what
// the client speaks, and it ignores a strategy too.
func (m *Manager) create_profile(ctx context.Context, token string, request Connect_request) (recovery.Target, api.Vpn_config, *Fault) {
	protocol, node_id := "", ""
	if request.Mode == Mode_manual {
		protocol, node_id = request.Protocol, request.Node_id
	} else {
		target, fault := m.choose_target(ctx, token, request)
		if fault != nil {
			return recovery.Target{}, api.Vpn_config{}, fault
		}
		protocol, node_id = target.Protocol, target.Node_id
	}

	// A desktop installation takes the account's free "PC" profile before
	// it spends a slot on a new one: a reinstall, or this machine signed
	// in again, is the same device.
	if id, ok := m.free_pc(ctx, token); ok {
		// Taken only once its config is had: a fetch that fails leaves
		// this installation without a profile, to choose again next time,
		// rather than holding one it never connected with.
		config, err := m.control_plane.Fetch_config(ctx, token, id, protocol, node_id, profile_for(effective_mode(m.local_state)))
		if err != nil {
			return recovery.Target{}, api.Vpn_config{}, classify_control_plane_error("fetch_config", err)
		}
		m.local_state.Client_id = id
		if serr := m.store.Save(m.local_state); serr != nil {
			return recovery.Target{}, api.Vpn_config{}, &Fault{Kind: Fault_configuration, Op: "adopt_profile", Err: serr}
		}
		m.logger.Info("took the account's free PC profile", "client_id", id)
		return recovery.Target{Node_id: node_id, Protocol: protocol}, config, nil
	}

	profile, err := m.control_plane.Create_profile(ctx, token, protocol, node_id, m.config.Profile_duration_days)
	if err != nil {
		return recovery.Target{}, api.Vpn_config{}, classify_control_plane_error("create_profile", err)
	}

	m.local_state.Client_id = profile.Client_id
	if serr := m.store.Save(m.local_state); serr != nil {
		return recovery.Target{}, api.Vpn_config{}, &Fault{Kind: Fault_configuration, Op: "create_profile", Err: serr}
	}
	m.name_new_pc(ctx, token, profile.Client_id)

	target := recovery.Target{Node_id: profile.Node_id, Protocol: profile.Protocol}
	offers := api.Recommendation{Node_id: profile.Node_id, Protocol: profile.Protocol, Alternatives: profile.Alternatives}
	if m.has_recommended {
		offers = m.recommended
		offers.Alternatives = append(append([]api.Alternative{}, m.recommended.Alternatives...), profile.Alternatives...)
	}
	m.alternatives = m.recovery_offers(offers, target)
	config := api.Vpn_config{Config: profile.Config, Protocol: profile.Protocol, Expires_at: profile.Expires_at}
	if want := profile_for(effective_mode(m.local_state)); want != "" {
		// Create_profile hands back the full VPN config. The person chose
		// the other profile of this node, so ask for that one.
		config, err = m.control_plane.Fetch_config(ctx, token, profile.Client_id, target.Protocol, target.Node_id, want)
		if err != nil {
			return recovery.Target{}, api.Vpn_config{}, classify_control_plane_error("fetch_config", err)
		}
	}
	return target, config, nil
}

// pinned_to_node keeps what the person picked by hand. A server picked by
// hand keeps only the alternatives on that server: another protocol on it is
// still the server they chose, another server is not. A protocol picked by
// hand keeps only that protocol, on whichever server. Automatic choice is
// what moves a connection to another node or protocol.
func (m *Manager) pinned_to_node(list []recovery.Target) []recovery.Target {
	node, protocol := m.local_state.Strategy.Node_id, m.local_state.Strategy.Protocol
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

// protocol_timeout is how long protocol may take to connect: the
// configured limit, or the protocol's own budget when it names a longer
// one. The whole connect's limit still bounds both.
func protocol_timeout(configured time.Duration, protocol protocols.Protocol) time.Duration {
	if budgeter, ok := protocol.(protocols.Connect_budgeter); ok && configured > 0 {
		if budget := budgeter.Connect_budget(); budget > configured {
			return budget
		}
	}
	return configured
}

// establish gets the named protocol adapter, connects it within
// Protocol_timeout (or the protocol's own longer budget), and verifies the tunnel actually carries traffic. A
// protocol that started is not a connection: on a failed verification, the
// protocol is disconnected before returning, so a half-established tunnel
// is never left behind with nothing watching it.
func (m *Manager) establish(ctx context.Context, target recovery.Target, config api.Vpn_config) (protocols.Protocol, *Fault) {
	protocol, err := m.protocols.Get(target.Protocol)
	if err != nil {
		return nil, classify_protocol_lookup_error("connect", err)
	}

	connect_config := m.connection_config(target, config)

	connect_ctx := ctx
	var cancel context.CancelFunc
	if timeout := protocol_timeout(m.config.Protocol_timeout, protocol); timeout > 0 {
		connect_ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	m.yield_adapter(ctx, protocol)
	err = protocol.Connect(connect_ctx, connect_config)
	if cancel != nil {
		cancel()
	}
	if err != nil {
		m.failed_observation(ctx, target, err, false)
		return nil, classify_connect_error("connect", err)
	}

	if err := diagnostics.Verify(ctx, m.prober); err != nil {
		// Whether the transport reached the node at all is the first
		// thing anyone asks of a tunnel that is up and carries nothing;
		// read it before the tunnel is taken down.
		if reporter, ok := protocol.(protocols.Transport_reporter); ok {
			if report := reporter.Transport_report(); report != "" {
				m.logger.Warn("verification failed", "node_id", target.Node_id, "protocol", target.Protocol, "transport", report)
				err = fmt.Errorf("%w (%s)", err, report)
			}
		}
		_ = protocol.Disconnect(ctx)
		m.failed_observation(ctx, target, err, true)
		return nil, classify_verify_error("connect", err)
	}

	return protocol, nil
}

// new_session builds the Session for a newly established target. In the
// practically-never case that the id generator itself is broken (see
// default_session_id), it disconnects the protocol that just came up and
// reports the failure instead of starting a session nothing could later
// correlate with anything.
func (m *Manager) new_session(ctx context.Context, now time.Time, target recovery.Target, protocol protocols.Protocol) (Session, *Fault) {
	id := m.new_session_id()
	if !session_id_usable(id) {
		_ = protocol.Disconnect(ctx)
		return Session{}, &Fault{Kind: Fault_platform, Op: "connect", Err: errors.New("connection: the session id generator did not produce a usable id")}
	}
	session := Session{Id: id, Node_id: target.Node_id, Protocol: target.Protocol, Started_at: now, Tunnel: effective_mode(m.local_state)}
	if m.has_recommended && m.recommended.Node_id == target.Node_id {
		session.Node_name = m.recommended.Node_name
		session.Country = m.recommended.Country
		session.City = m.recommended.City
		session.Reason = m.recommended.Reason
		session.Goals = append([]api.Goal_outcome(nil), m.recommended.Goals...)
	}
	return session, nil
}

// settle_connected lands a newly established target in State_connected:
// build the session, record success with the policy, remember the target
// for next time, and emit completed. Both Connect and Switch reach
// State_connected through here, because everything that must happen the
// moment a tunnel is verified is the same regardless of which flow got
// there.
func (m *Manager) settle_connected(ctx context.Context, now time.Time, target recovery.Target, protocol protocols.Protocol, config api.Vpn_config, op string, completed events.Kind, completed_data any) error {
	session, fault := m.new_session(ctx, m.now(), target, protocol)
	if fault != nil {
		return m.fail(now, fault)
	}

	m.protocol_handle = protocol
	m.current = target
	m.current_config = m.connection_config(target, config)
	m.set_session(session, true)
	m.baseline = diagnostics.Baseline{}
	m.monitor = probe_schedule{}
	m.degraded_since_set = false
	m.consecutive_probe_errors = 0
	m.last_reevaluation = now
	m.policy.Record_success(now, target)

	m.local_state.Last_node_id = target.Node_id
	m.local_state.Last_protocol = target.Protocol
	if err := m.store.Save(m.local_state); err != nil {
		// The tunnel is up; a failed save of the last-used hint is not
		// worth tearing it down over, but it must not vanish silently.
		m.publish(events.Kind_error, &Fault{Kind: Fault_configuration, Op: op, Err: err})
	}

	m.established_observation(ctx, target)
	m.set_state(State_connected)
	m.publish(completed, completed_data)
	return nil
}

// fail lands the machine in State_failed and reports fault.
// State_disconnected means nobody has asked for a connection; every path
// through Connect or Switch that did not end in State_connected means
// somebody asked and it did not work, which is State_failed regardless of
// which step failed.
func (m *Manager) fail(now time.Time, fault *Fault) error {
	m.stop_companion(context.Background())
	m.set_state(State_failed)
	m.publish(events.Kind_error, fault)
	return fault
}

// --- Disconnect ------------------------------------------------------

// Disconnect stops the current protocol and returns to State_disconnected.
// CLIENT_CORE.md section 44. Disconnecting when already disconnected is not
// an error and starts nothing (CLIENT_CORE.md 35).
// Set_connect_on_start records whether the person wants to be connected,
// so a reboot -- or a service restart after a crash -- puts them back where
// they left off. It runs under op_mu because Connect saves the same state:
// written outside the lock, a "no" recorded while a connect was in flight
// would be overwritten by that connect's save of the old "yes".
func (m *Manager) Set_connect_on_start(want bool) error {
	m.cancel_monitor_probe()
	if !want {
		// "Stay off" is said with a disconnect; it must not wait out the
		// connect it is about to undo.
		m.abandon_connect()
	}
	m.op_mu.Lock()
	defer m.op_mu.Unlock()

	local, err := m.store.Load()
	if err != nil {
		return err
	}
	if local.Connect_on_start == want {
		return nil
	}
	local.Connect_on_start = want
	if err := m.store.Save(local); err != nil {
		return err
	}
	m.local_state = local
	return nil
}

// Set_strategy records what the connection is for. It takes effect at the
// next recommendation; Apply_strategy moves a connection that is already
// up.
func (m *Manager) Set_strategy(strategy state.Strategy) error {
	m.cancel_monitor_probe()
	m.op_mu.Lock()
	defer m.op_mu.Unlock()

	local, err := m.store.Load()
	if err != nil {
		return err
	}
	local.Strategy = strategy
	if err := m.store.Save(local); err != nil {
		return err
	}
	m.local_state = local
	return nil
}

// connection_config is what a protocol is handed for target: the node's
// config and what the tunnel is to carry.
func (m *Manager) connection_config(target recovery.Target, config api.Vpn_config) protocols.Connection_config {
	return protocols.Connection_config{
		Node_id:  target.Node_id,
		Protocol: target.Protocol,
		Data:     []byte(config.Config),
		Split:    split_for(m.local_state),
	}
}

// split_for decides what the tunnel carries. Games chosen alone put only
// those games through it. A goal that is about the whole machine --
// anonymity, or getting past blocks -- keeps the whole machine in the
// tunnel, games included: a browser left outside would defeat it. A game
// whose anti-cheat reacts to the packet driver does the same and takes the
// driver off the machine. A selection the catalog cannot read falls back
// to the whole machine rather than to nothing.
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
	if m.retry_current(ctx) {
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

// Strategy reports what Set_strategy last recorded.
//
// It does not take op_mu: the store serialises its own reads and writes,
// and a window asking what the strategy is must not wait behind a connect
// that may take a minute.
func (m *Manager) Strategy() (state.Strategy, error) {
	local, err := m.store.Load()
	if err != nil {
		return state.Strategy{}, err
	}
	return local.Strategy, nil
}

// Apply_strategy asks the control plane again for a connection that is up
// in auto mode, and switches to its answer if that is somewhere else. A
// connection that is not up, or that the person pinned in manual mode, is
// left alone: the next connect asks with the new strategy anyway.
func (m *Manager) Apply_strategy(ctx context.Context) error {
	m.cancel_monitor_probe()
	m.op_mu.Lock()
	defer m.op_mu.Unlock()

	current := m.get_state()
	if (current != State_connected && current != State_degraded) || m.mode != Mode_auto {
		return nil
	}
	token, err := m.token.Token(ctx)
	if err != nil {
		return &Fault{Kind: Fault_authentication, Op: "apply_strategy", Err: err}
	}
	target, fault := m.choose_target(ctx, token, Connect_request{Mode: Mode_auto})
	if fault != nil {
		return fault
	}
	if target == m.current {
		// Same node, new account of why: the session must not keep saying
		// it was picked by hand after the person turned automatic back on.
		m.state_mu.Lock()
		m.session.Reason = m.recommended.Reason
		m.session.Goals = append([]api.Goal_outcome(nil), m.recommended.Goals...)
		m.state_mu.Unlock()
		return nil
	}

	// A goal changed by a person starts the recovery budget over, as a
	// connect does: it is not a failure, and it must neither spend the
	// budget Tick relies on nor be refused as flapping.
	m.policy.Reset()
	previous := m.current
	now := m.now()
	m.set_state(State_switching)
	m.publish(events.Kind_switch_started, target)
	err = m.switch_loop(ctx, now, target)
	if err == nil || m.get_state() != State_failed {
		return err
	}

	// The switch tears the old tunnel down before building the new one, so
	// a new target that never came up has left the person with nothing --
	// for having touched a goal. Go back to where they were, and report
	// why the move failed.
	config, cerr := m.control_plane.Fetch_config(ctx, token, m.local_state.Client_id, previous.Protocol, previous.Node_id, profile_for(effective_mode(m.local_state)))
	if cerr != nil {
		return err
	}
	m.policy.Reset()
	m.has_recommended = false
	m.set_state(State_connecting)
	m.publish(events.Kind_connection_started, nil)
	if rerr := m.connect_loop(ctx, m.now(), token, previous, config); rerr != nil {
		return rerr
	}
	return err
}

// Connect_on_start reports what Set_connect_on_start last recorded.
func (m *Manager) Connect_on_start() (bool, error) {
	m.op_mu.Lock()
	defer m.op_mu.Unlock()
	local, err := m.store.Load()
	if err != nil {
		return false, err
	}
	return local.Connect_on_start, nil
}

func (m *Manager) Disconnect(ctx context.Context) error {
	m.abandon_connect()
	m.cancel_monitor_probe()
	m.op_mu.Lock()
	defer m.op_mu.Unlock()

	current := m.get_state()
	if current == State_disconnected {
		return nil
	}
	// A failed connect has nothing up to take down: it goes straight to
	// disconnected, which is what the person asked for, not a refusal.
	if current != State_failed {
		if !Allowed(current, State_disconnecting) {
			return Err_busy
		}
		m.set_state(State_disconnecting)
	}

	m.end_observation(ctx, "manual", false)
	m.stop_companion(ctx)
	var fault *Fault
	if m.protocol_handle != nil {
		if err := m.protocol_handle.Disconnect(ctx); err != nil {
			// Refusing to let go of a connection the person asked to end
			// is the worse failure: the state still moves to
			// State_disconnected, and the error is reported rather than
			// silently dropped.
			fault = &Fault{Kind: Fault_protocol_connection, Op: "disconnect", Err: err}
		}
		m.protocol_handle = nil
	}
	m.clear_session()
	m.set_state(State_disconnected)

	if fault != nil {
		m.publish(events.Kind_error, fault)
		return fault
	}
	return nil
}

func (m *Manager) set_abandon(abandon context.CancelFunc) {
	m.abandon_mu.Lock()
	m.abandon = abandon
	m.abandon_mu.Unlock()
}

// abandon_connect cancels a connect in progress, if there is one.
func (m *Manager) abandon_connect() {
	m.abandon_mu.Lock()
	abandon := m.abandon
	m.abandon_mu.Unlock()
	if abandon != nil {
		abandon()
	}
}

func (m *Manager) clear_session() {
	m.set_session(Session{}, false)
	m.current = recovery.Target{}
	m.current_config = protocols.Connection_config{}
	m.degraded_since_set = false
	m.consecutive_probe_errors = 0
	m.retried_at, m.retried_target = time.Time{}, recovery.Target{}
}

// --- Switch ----------------------------------------------------------

// Switch moves the connection to target. CLIENT_CORE.md section 45.
// Restoring the previous connection when a switch fails is named there as
// something to implement "only after a safe strategy is defined" -- it is
// not implemented here; a future stage that defines that strategy owns it.
func (m *Manager) Switch(ctx context.Context, target recovery.Target) error {
	m.cancel_monitor_probe()
	m.op_mu.Lock()
	defer m.op_mu.Unlock()

	current := m.get_state()
	if !Allowed(current, State_switching) {
		return Err_busy
	}

	now := m.now()
	m.set_state(State_switching)
	m.publish(events.Kind_switch_started, target)
	return m.switch_loop(ctx, now, target)
}

// switch_loop is the mechanical part of a switch: it assumes the caller has
// already entered State_switching and announced Kind_switch_started, and
// has already decided target is worth trying. Switch does both itself with
// target already known; escalate does them before it knows whether
// recovery.Policy can even name a target, because entering State_switching
// is the only way this package's transition table lets a connected or
// degraded machine reach State_failed -- there is no direct edge for it,
// and there should not be one: "attempting to leave" is genuinely a
// different state from "connected" even when the attempt finds nowhere to
// go.
func (m *Manager) switch_loop(ctx context.Context, now time.Time, target recovery.Target) error {
	m.end_observation(ctx, "replaced", false)
	token, err := m.token.Token(ctx)
	if err != nil {
		return m.fail(now, &Fault{Kind: Fault_authentication, Op: "switch", Err: err})
	}

	for {
		if m.protocol_handle != nil {
			// Leaving this target regardless of what stopping it reports:
			// a failure to stop cleanly does not cancel the switch.
			_ = m.protocol_handle.Disconnect(ctx)
			m.protocol_handle = nil
		}

		config, cerr := m.control_plane.Fetch_config(ctx, token, m.local_state.Client_id, target.Protocol, target.Node_id, profile_for(effective_mode(m.local_state)))
		var fault *Fault
		var protocol protocols.Protocol
		if cerr != nil {
			fault = classify_control_plane_error("switch", cerr)
		} else {
			protocol, fault = m.establish(ctx, target, config)
			if fault == nil {
				return m.settle_connected(ctx, now, target, protocol, config, "switch", events.Kind_switch_completed, target)
			}
		}

		m.publish(events.Kind_switch_failed, fault)
		m.policy.Record_failure(now, target)

		next, nerr := m.policy.Next_target(now, recovery.Reason_hard_fail, target, m.alternatives)
		if nerr != nil {
			return m.fail(now, fault)
		}
		target = next
	}
}

// retry_current re-establishes the current target once, reusing the config
// already fetched for it: a retry is not a new choice of node, so nothing
// about it needs the control plane again. It reports true only if the
// tunnel comes back up and verifies.
func (m *Manager) retry_current(ctx context.Context) bool {
	m.end_observation(ctx, "replaced", false)
	// While the tunnel is being rebuilt it is not connected, and the
	// window must not say it is: every rebuild -- a new split, another
	// profile, a retry after a loss -- shows as switching until it is
	// verified again. A rebuild that fails stays switching for the caller
	// to settle.
	if state := m.get_state(); state == State_connected || state == State_degraded {
		m.set_state(State_switching)
	}
	if m.protocol_handle != nil {
		_ = m.protocol_handle.Disconnect(ctx)
		m.protocol_handle = nil
	}

	protocol, err := m.protocols.Get(m.current.Protocol)
	if err != nil {
		return false
	}

	connect_ctx := ctx
	var cancel context.CancelFunc
	if timeout := protocol_timeout(m.config.Protocol_timeout, protocol); timeout > 0 {
		connect_ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	m.yield_adapter(ctx, protocol)
	err = protocol.Connect(connect_ctx, m.current_config)
	if cancel != nil {
		cancel()
	}
	if err != nil {
		m.failed_observation(ctx, m.current, err, false)
		m.logger.Warn("retry on the same node failed to connect", "node_id", m.current.Node_id, "protocol", m.current.Protocol, "err", err)
		return false
	}

	if err := diagnostics.Verify(ctx, m.prober); err != nil {
		m.failed_observation(ctx, m.current, err, true)
		m.logger.Warn("retry on the same node failed verification", "node_id", m.current.Node_id, "protocol", m.current.Protocol, "err", err)
		_ = protocol.Disconnect(ctx)
		return false
	}

	m.protocol_handle = protocol
	m.established_observation(ctx, m.current)
	if m.get_state() == State_switching {
		m.set_state(State_connected)
	}
	return true
}

// --- Tick ------------------------------------------------------------

// Tick is given the time and does all the monitoring, the degradation
// window and the re-evaluation. It is the only place this package reasons
// about the passage of time, and it never reads a clock of its own.
func (m *Manager) Tick(ctx context.Context, now time.Time) {
	m.op_mu.Lock()
	defer m.op_mu.Unlock()
	m.tick_locked(ctx, now)
}

// Network_changed reacts to the machine's network changing under a running
// tunnel: the protocol, when it can (protocols.Repinner), moves its transport
// route onto the current path without the tunnel being torn down, and then
// one monitoring pass runs, so a tunnel that still does not work goes through
// the usual retry and recovery. A repin failure is logged, not fatal: the
// monitoring pass is what decides whether the tunnel is really broken. With
// no tunnel up, or one still being established, it does nothing -- a connect
// in progress resolves and pins on its own.
func (m *Manager) Network_changed(ctx context.Context, now time.Time) {
	m.cancel_monitor_probe()
	m.op_mu.Lock()
	defer m.op_mu.Unlock()

	current := m.get_state()
	if current != State_connected && current != State_degraded {
		return
	}
	// The repin and the pass share one hold of op_mu: Tick takes the same
	// lock, so the pass is the lock-free body tick_locked.
	m.monitor = probe_schedule{}
	m.baseline = diagnostics.Baseline{}
	m.degraded_since_set = false
	if repinner, ok := m.protocol_handle.(protocols.Repinner); ok {
		if err := repinner.Repin(ctx); err != nil {
			m.logger.Warn("network changed; moving the endpoint route failed", "error", err)
		} else {
			m.logger.Info("network changed; endpoint route follows the current path")
		}
	}
	m.tick_locked(ctx, now)
}

// tick_locked is Tick's body; the caller holds op_mu.
func (m *Manager) tick_locked(ctx context.Context, now time.Time) {
	m.record_monitor_tick(now)
	// Whatever the pass decides, the AntiZapret companion follows it.
	defer m.keep_companion(ctx, now)
	current := m.get_state()
	if current != State_connected && current != State_degraded {
		m.degraded_pending = false
		return // nothing is being monitored outside these two states
	}

	sample, err := m.monitor_probe(ctx)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		m.monitor.observe(now, false, m.config.Monitor_interval, m.config.Monitor_max_interval)
		m.evidence_loss_reason = "unknown"
		if errors.Is(err, diagnostics.Err_no_internet) {
			m.evidence_loss_reason = "reachability_lost"
		}
		if errors.Is(err, diagnostics.Err_tunnel_down) {
			m.evidence_loss_reason = "tunnel_stopped"
		}
		m.consecutive_probe_errors++
		reason := "measurement_failed"
		if errors.Is(err, diagnostics.Err_no_internet) {
			reason = "no_internet"
		}
		if errors.Is(err, diagnostics.Err_tunnel_down) {
			reason = "tunnel_down"
		}
		m.logger.Warn("connection probe failed", "reason", reason, "consecutive_failures", m.consecutive_probe_errors, "recovery_threshold", probe_errors_before_lost)
		if m.consecutive_probe_errors < probe_errors_before_lost {
			return // an unknown is not a reason to move a person's connection
		}
		m.consecutive_probe_errors = 0
		m.handle_lost(ctx, now)
		return
	}
	m.consecutive_probe_errors = 0
	m.baseline.Observe(sample)
	detail := diagnostics.Degradation_of(sample, m.baseline, diagnostics.Default_thresholds())
	m.set_sample(sample, detail)
	verdict := diagnostics.Judge(sample, m.baseline, diagnostics.Default_thresholds())
	m.monitor.observe(now, verdict == diagnostics.Verdict_healthy && sample.Dns_ok && m.baseline.Ready(), m.config.Monitor_interval, m.config.Monitor_max_interval)

	pending := m.degraded_pending
	m.degraded_pending = false
	switch verdict {
	case diagnostics.Verdict_healthy:
		m.evidence_sleep_seen = false
		if current == State_degraded {
			m.degraded_since_set = false
			m.set_state(State_connected) // no switch, no event beyond the state change
		}
		m.maybe_reevaluate(ctx, now)

	case diagnostics.Verdict_degraded:
		// One bad sample on a healthy connection is a blip until the
		// re-check, which a bad sample brings forward to the fast interval,
		// agrees. A single dropped burst of packets must not paint the
		// connection red; it is still counted and shown.
		if current == State_connected && !pending {
			m.degraded_pending = true
			m.publish(events.Kind_connection_blip, detail)
			return
		}
		if current != State_degraded || !m.degraded_since_set {
			m.degraded_since = now
			m.degraded_since_set = true
			m.held_in_match = false
			m.set_state(State_degraded)
			m.publish(events.Kind_connection_degraded, detail)
			return
		}
		if m.mode == Mode_auto && m.degraded_since_set && now.Sub(m.degraded_since) >= m.config.Recovery.Degradation_window {
			if m.in_match.Load() {
				if !m.held_in_match {
					m.held_in_match = true
					m.logger.Info("degraded during a match; staying on the node", "node_id", m.current.Node_id)
				}
				return
			}
			m.escalate(ctx, now, recovery.Reason_degraded)
		}

	case diagnostics.Verdict_lost:
		m.evidence_loss_reason = "reachability_lost"
		if !sample.Tunnel_up {
			m.evidence_loss_reason = "tunnel_stopped"
		}
		m.handle_lost(ctx, now)
	}
}

// handle_lost implements CLIENT_CORE.md section 20: a lost verdict does not
// immediately move a person off their current target. It is retried once;
// only a retry that itself fails is treated as a hard failure and handed to
// recovery, and only in auto mode.
func (m *Manager) handle_lost(ctx context.Context, now time.Time) {
	if m.evidence != nil {
		m.evidence.Network_changed(ctx)
	}
	m.end_observation(ctx, m.loss_observation_reason(now), true)
	m.publish(events.Kind_connection_lost, nil)

	flapping := m.retried_target == m.current && !m.retried_at.IsZero() && now.Sub(m.retried_at) < retry_flap_window
	if flapping {
		m.logger.Warn("lost again soon after a retry; not retrying the same target", "node_id", m.current.Node_id, "protocol", m.current.Protocol)
		// What retry_current would have taken down goes down here: the
		// tunnel that does not hold is not left up under whatever follows.
		m.set_state(State_switching)
		if m.protocol_handle != nil {
			_ = m.protocol_handle.Disconnect(ctx)
			m.protocol_handle = nil
		}
	} else if m.retry_current(ctx) {
		m.retried_at, m.retried_target = now, m.current
		return // the retry established and verified; carry on
	}

	m.policy.Record_failure(now, m.current)
	if m.mode != Mode_auto {
		// CLIENT_CORE.md 47: the person chose this node; the manager
		// reports the loss and stops here rather than moving them off a
		// choice they were explicitly given. The tunnel is gone, and the
		// state says so instead of staying connected. A failure is
		// reached through switching, which a retry has already set and a
		// target not retried has not.
		m.set_state(State_switching)
		m.fail(now, &Fault{Kind: Fault_protocol_connection, Op: "tick", Err: errors.New("connection: the chosen server stopped answering and did not come back")})
		return
	}
	m.escalate(ctx, now, recovery.Reason_hard_fail)
}

// escalate asks recovery.Policy for another target and switches to it.
//
// What happens when the policy refuses depends on whether there is still a
// tunnel. Reason_degraded means there is: the connection is worse than it
// should be and is carrying traffic anyway. Err_too_soon there is not a
// failure at all but the anti-flapping window saying "not yet", which a
// later tick will ask again -- turning it into State_failed would use the
// guard that exists to protect a connection as the thing that declares it
// dead. The other refusals mean recovery has nowhere to go, and a degraded
// tunnel is still better than none, so the machine stays where it is and
// says so rather than reporting a failure a person would act on by
// reconnecting something that already works, badly.
//
// Reason_hard_fail means the tunnel is gone, and there State_failed is the
// truth. It is reached through State_switching because connected and
// degraded have no direct edge to failed in this package's table (only
// switching does), and "the machine is attempting to leave" is true the
// moment recovery is consulted.
func (m *Manager) escalate(ctx context.Context, now time.Time, reason recovery.Reason) {
	next, err := m.policy.Next_target(now, reason, m.current, m.alternatives)
	if err != nil {
		if reason == recovery.Reason_degraded {
			if !errors.Is(err, recovery.Err_too_soon) {
				m.publish(events.Kind_error, fault_from_recovery_error("tick", err))
			}
			return
		}
		m.set_state(State_switching)
		m.publish(events.Kind_switch_started, nil)
		m.fail(now, fault_from_recovery_error("tick", err))
		return
	}

	m.set_state(State_switching)
	m.publish(events.Kind_switch_started, next)
	_ = m.switch_loop(ctx, now, next)
}

// maybe_reevaluate offers a better option once per Reevaluation_interval
// while the connection is healthy. CLIENT_AUTO_SWITCH.md section 15 says
// "offer a switch" -- offering is the whole instruction: switching a
// healthy connection because something might be marginally better is
// exactly the flapping this design forbids, and nobody asked for it. Manual
// mode never reaches here for the connect target it was given, matching
// CLIENT_CORE.md section 47.
func (m *Manager) maybe_reevaluate(ctx context.Context, now time.Time) {
	// Nor is a match the moment to offer one; the offer waits for it to
	// end.
	if m.mode != Mode_auto || m.in_match.Load() {
		return
	}
	if now.Sub(m.last_reevaluation) < m.config.Reevaluation_interval {
		return
	}
	m.last_reevaluation = now

	token, err := m.token.Token(ctx)
	if err != nil {
		return // an unsolicited check failing is not worth reporting as a Fault
	}
	recommendation, err := m.control_plane.Recommend(ctx, token, m.recommend_request())
	if err != nil {
		return
	}
	// The same node over a protocol that would carry the whole machine is
	// no better while the games go alone.
	same_node_whole := recommendation.Node_id == m.current.Node_id && effective_mode(m.local_state) == Tunnel_games &&
		!m.carries_alone(recommendation.Protocol) && m.carries_alone(m.current.Protocol)
	if !same_node_whole && (recommendation.Node_id != m.current.Node_id || recommendation.Protocol != m.current.Protocol) {
		m.publish(events.Kind_better_option, recovery.Target{Node_id: recommendation.Node_id, Protocol: recommendation.Protocol})
	}
}

// --- Run ---------------------------------------------------------------

// Run checks local tunnel health at Monitor_interval and spaces full probes
// adaptively. Explicit Tick calls retain their unconditional probe semantics.
func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.config.Monitor_interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.monitor_tick(ctx, m.now())
			ticker.Reset(m.config.Monitor_interval)
		}
	}
}

// obfuscated_first orders protocols for games: AmneziaWG before the rest.
func obfuscated_first(protocol string) int {
	if protocol == "amneziawg" {
		return 0
	}
	return 1
}

// Reorder protocols within each node's existing offer slots, preserving node priority.
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
	// What this network reported problems with is tried after the rest.
	ordered := prefer_obfuscated(kept)
	clean := without_reported(ordered, recommendation.Evidence, Evidence_mode(effective_mode(m.local_state)))
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
