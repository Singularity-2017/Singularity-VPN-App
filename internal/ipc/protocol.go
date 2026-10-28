package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"singularity-vpn-app/internal/journal"
	"singularity-vpn-app/internal/matchserver"
	"singularity-vpn-app/internal/statistics"
)

// Pipe_name is the one address this protocol is spoken on.
const Pipe_name = `\\.\pipe\SingularityVPN`

// The frame kinds. A frame the service sends carries Type, so a reader on
// the other end can tell a reply to its own request apart from an event
// that arrived unasked.
const (
	Type_response = "response"
	Type_event    = "event"
)

// Request is one line a client sends. Params is left as raw JSON because
// what it decodes into depends on Command, and that decoding is the
// server's job, not the transport's.
type Request struct {
	Id      string          `json:"id"`
	Command string          `json:"command"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response answers exactly one Request, correlated by Id.
//
// Id is chosen by the caller and echoed back unchanged; the server never
// invents one. A request that arrived with an empty Id is still answered --
// with an empty Id -- because refusing it outright would be a worse failure
// than a caller that cannot correlate its own traffic.
type Response struct {
	Type   string          `json:"type"` // always Type_response
	Id     string          `json:"id"`
	Ok     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error_body     `json:"error,omitempty"`
}

// Error_body is what Response carries when Ok is false.
type Error_body struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Event is something that happened, delivered unasked once a connection has
// subscribed. It mirrors events.Event, translated to the wire: Data is raw
// JSON rather than any, for the same reason Request.Params is.
type Event struct {
	Type       string          `json:"type"` // always Type_event
	Kind       string          `json:"kind"`
	At         time.Time       `json:"at"`
	Session_id string          `json:"session_id,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// The command set. This is the whole privileged surface a caller on the
// other end of the pipe may invoke. docs/WINDOWS_CLIENT.md section 45 is
// the rule it exists to keep: the service must offer a limited set of
// operations and must never become a general-purpose executor of
// privileged commands. Adding a command is a deliberate edit to this
// block, not something reachable from the wire.
//
// Deliberately absent:
//
//   - switch -- recovery chooses a different target on its own. A person
//     picks a server through set_strategy instead, which only asks the
//     control plane for that server: the control plane still decides
//     whether this account may be put on it.
//   - settings -- Connect_on_start is set by connect and cleared by
//     disconnect and sign_out; it is not a separate knob.
const (
	Command_status     = "status"
	Command_connect    = "connect"
	Command_disconnect = "disconnect"
	Command_sign_in    = "sign_in"
	Command_sign_out   = "sign_out"
	Command_subscribe  = "subscribe"
	Command_version    = "version"

	Command_strategy     = "strategy"
	Command_set_strategy = "set_strategy"
	Command_servers      = "servers"
	Command_ping_servers = "ping_servers"
	Command_games        = "games"
	Command_set_games    = "set_games"
	// Command_answer_match_server is the player's answer about the match
	// server offered on a game's card.
	Command_answer_match_server = "answer_match_server"
	Command_tunnel_mode         = "tunnel_mode"

	Command_set_tunnel_mode = "set_tunnel_mode"

	// Command_profiles lists the account's profiles; Command_set_profile
	// makes this installation connect with another of them.
	Command_profiles    = "profiles"
	Command_set_profile = "set_profile"

	Command_analyze  = "analyze"
	Command_analysis = "analysis"

	Command_journal = "journal"
)

// Journal_result answers Command_journal: the service's recent operations,
// oldest first.
type Journal_result struct {
	Entries []journal.Entry `json:"entries"`
}

// Strategy is what the connection is for -- see state.Strategy, which it
// mirrors on the wire. The zero value asks for nothing in particular.
type Strategy struct {
	Anonymity string `json:"anonymity,omitempty"` // "" off, or light, standard, high
	Bypass    bool   `json:"bypass,omitempty"`
	Country   string `json:"country,omitempty"` // ISO 3166-1 alpha-2, "" for anywhere
	Route     bool   `json:"route,omitempty"`
	Node_id   string `json:"node_id,omitempty"`  // a server picked by hand, "" for automatic
	Protocol  string `json:"protocol,omitempty"` // a protocol picked by hand, "" for automatic
}

// Strategy_protocols are the protocols a person can pick by hand.
var Strategy_protocols = []string{"amneziawg", "wireguard", "openvpn"}

// Validate refuses an anonymity level that is not one of the three, a
// country that is not two capital letters, and a node id that is not a
// plain short token.
func (s Strategy) Validate() error {
	switch s.Anonymity {
	case "", "light", "standard", "high":
	default:
		return fmt.Errorf("ipc: strategy: anonymity %q is not light, standard or high", s.Anonymity)
	}
	if s.Country != "" {
		if len(s.Country) != 2 || s.Country[0] < 'A' || s.Country[0] > 'Z' || s.Country[1] < 'A' || s.Country[1] > 'Z' {
			return fmt.Errorf("ipc: strategy: country %q is not a two-letter code", s.Country)
		}
	}
	if len(s.Node_id) > 64 {
		return errors.New("ipc: strategy: node_id is too long")
	}
	for _, c := range s.Node_id {
		if !(c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return fmt.Errorf("ipc: strategy: node_id %q carries a character a node id does not", s.Node_id)
		}
	}
	if s.Protocol != "" && !slices.Contains(Strategy_protocols, s.Protocol) {
		return fmt.Errorf("ipc: strategy: protocol %q is not one of %s", s.Protocol, strings.Join(Strategy_protocols, ", "))
	}
	return nil
}

// What a tunnel carries. Tunnel_vpn and Tunnel_antizapret are the two
// profiles a person chooses between; Tunnel_games is only ever reported, by
// Status_result.Tunnel, when games alone are put through the tunnel.
const (
	Tunnel_vpn        = "vpn"
	Tunnel_antizapret = "antizapret"
	Tunnel_games      = "games"
)

// Tunnel_mode is which profile of the node the person wants: the whole
// machine through the tunnel, or only what is blocked.
type Tunnel_mode struct {
	Mode string `json:"mode"`
}

// Validate refuses anything but exactly "vpn" or "antizapret".
func (t Tunnel_mode) Validate() error {
	switch t.Mode {
	case Tunnel_vpn, Tunnel_antizapret:
		return nil
	}
	return fmt.Errorf("ipc: tunnel mode %q is not vpn or antizapret", t.Mode)
}

// Games are the catalog ids of the games carried through the tunnel. An
// empty list carries the whole machine, as before any game was chosen.
type Games struct {
	Games []string `json:"games"`
	// Region is the region of the one selected game the person boosts
	// toward, a region id from the catalog; empty for wherever it plays.
	Region string `json:"region,omitempty"`
	// Pings is, in an answer, the last round trip measured to the server
	// each game was playing on, for the games that have one. A set
	// request carries none.
	Pings []Game_ping `json:"pings,omitempty"`
	// In_match is, in an answer, whether a game is in a match: changing
	// the server or the games would end it.
	In_match bool `json:"in_match,omitempty"`
	// Match_servers is, in an answer, per selected game, the match server
	// waiting for the player to confirm and how often the detection was
	// right; a game with neither is absent.
	Match_servers []Match_server `json:"match_servers,omitempty"`
}

// Match_server is one game's detected match server and its record.
type Match_server struct {
	Game string `json:"game"`
	// Server is the address and port the games streamed the most to in
	// the current or last match, offered for the player to check against
	// what the game shows; empty when nothing waits for an answer.
	// Packets of Total went to its address, Runner_up to the next one.
	Server    string `json:"server,omitempty"`
	Packets   int    `json:"packets,omitempty"`
	Runner_up int    `json:"runner_up,omitempty"`
	Total     int    `json:"total,omitempty"`
	// Right of Confirmed answers found the detection right; a dismissal
	// is in neither.
	Right     int `json:"right,omitempty"`
	Confirmed int `json:"confirmed,omitempty"`
}

// Match_server_answer is the player's answer about the server offered for
// a game: Answer is "yes", "no" or "dismiss", Server the one they were
// shown, and Shown, on a "no", the address the game shows if they typed it.
type Match_server_answer struct {
	Game   string `json:"game"`
	Server string `json:"server"`
	Answer string `json:"answer"`
	Shown  string `json:"shown,omitempty"`
}

// Validate refuses an answer that is not one of the three, a game id that
// is not a token, and a server or address that does not parse.
func (m Match_server_answer) Validate() error {
	if !is_game_token(m.Game) {
		return fmt.Errorf("ipc: match server answer: game %q is not a game id", m.Game)
	}
	if _, err := netip.ParseAddrPort(m.Server); err != nil {
		return fmt.Errorf("ipc: match server answer: server %q is not an address and port", m.Server)
	}
	switch m.Answer {
	case "yes", "dismiss":
	case "no":
		if _, err := matchserver.Parse_shown(m.Shown); err != nil {
			return fmt.Errorf("ipc: match server answer: %w", err)
		}
	default:
		return fmt.Errorf("ipc: match server answer: %q is not yes, no or dismiss", m.Answer)
	}
	return nil
}

// Game_ping is one game's last measured round trip to its match server,
// through the tunnel, and when it was measured. When the match server
// ignores echo, Rtt_ms is the sum of two measurements, both present:
// First_mile_ms from this machine to the node, and Node_ms from the node
// to the game's region.
type Game_ping struct {
	Game          string    `json:"game"`
	Rtt_ms        float64   `json:"rtt_ms"`
	First_mile_ms *float64  `json:"first_mile_ms,omitempty"`
	Node_ms       *float64  `json:"node_ms,omitempty"`
	At            time.Time `json:"at"`
}

// Validate refuses more ids than any catalog has games, and an id or a
// region that is not a short lower-case token. Whether the catalog knows
// them is the service's to say.
func (g Games) Validate() error {
	if len(g.Games) > 64 {
		return errors.New("ipc: games: too many games")
	}
	for _, id := range g.Games {
		if !is_game_token(id) {
			return fmt.Errorf("ipc: games: id %q is empty, too long or carries a character a game id does not", id)
		}
	}
	if g.Region != "" && !is_game_token(g.Region) {
		return fmt.Errorf("ipc: games: region %q is too long or carries a character a region does not", g.Region)
	}
	return nil
}

// is_game_token reports whether s is a short lower-case token.
func is_game_token(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// Goal_outcome is the control plane's account of one goal, passed on as it
// came: whether it shaped the choice of node and, when it did not, why.
type Goal_outcome struct {
	Goal     string `json:"goal"`
	Applied  bool   `json:"applied"`
	Why      string `json:"why,omitempty"`
	Compared int    `json:"compared,omitempty"`
}

// Server is one entry of Servers_result.
type Server struct {
	Node_id   string `json:"node_id"`
	Name      string `json:"name,omitempty"`
	Country   string `json:"country,omitempty"`
	Region    string `json:"region,omitempty"`
	City      string `json:"city,omitempty"`
	Available bool   `json:"available"`
}

// Servers_result answers Command_servers.
type Servers_result struct {
	Servers []Server `json:"servers"`
}

// Server_ping is the latency to one server: the round trip of an ICMP echo,
// in milliseconds, as measured from this machine outside the tunnel. Node_id
// is the one Server carries; the server's address is not on the wire, here
// or in Server.
type Server_ping struct {
	Node_id string  `json:"node_id"`
	Rtt_ms  float64 `json:"rtt_ms"`
}

// Ping_servers_result answers Command_ping_servers. Pings holds only the
// servers that answered, sorted by node id; a server that did not is absent,
// not present with a zero, and the list is empty -- never null -- when none
// did.
type Ping_servers_result struct {
	Pings []Server_ping `json:"pings"`
}

// Reach_check is one probed target in an Analysis_result: how far each of
// the three steps got, and what that comes to. Dns, Tcp and Tls are
// "open", "blocked" or "not_tested"; Failure names how the step that failed
// failed (no_address, refused, timeout, reset, bad_certificate) and is
// absent when none did. Verdict is "open", "blocked", "no_network" or
// "no_answer", and Method -- "dns", "tcp" or "tls" -- is present only on a
// blocked one. Control marks a host inside the caller's own country, which
// is never called blocked.
type Reach_check struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Host    string `json:"host"`
	Control bool   `json:"control"`
	Dns     string `json:"dns"`
	Tcp     string `json:"tcp"`
	Tls     string `json:"tls"`
	Failure string `json:"failure,omitempty"`
	Verdict string `json:"verdict"`
	Method  string `json:"method,omitempty"`
}

// Analysis_result answers Command_analyze: what the network under the
// device blocks, measured directly with the tunnel down. Control is "ok",
// "failed" or "absent": whether hosts inside the caller's own country
// answered, which is what makes a failure elsewhere a block rather than a
// dead network.
type Analysis_result struct {
	Measured_at time.Time     `json:"measured_at"`
	Country     string        `json:"country,omitempty"`
	Control     string        `json:"control"`
	Checks      []Reach_check `json:"checks"`
}

// Last_analysis answers Command_analysis: the last result, without probing.
// Present is false when no successful measurement is available. Restored
// measurements and measurements predating a network change are marked stale.
type Last_analysis struct {
	Stale   bool             `json:"stale,omitempty"`
	Present bool             `json:"present"`
	Result  *Analysis_result `json:"result,omitempty"`
}

// Mode_auto and Mode_manual are the two values Connect_params.Mode may hold
// on the wire.
const (
	Mode_auto   = "auto"
	Mode_manual = "manual"
)

// Connect_params carries the mode as a string rather than connection.Mode,
// whose zero value is Mode_auto. On the wire, a missing field and "the
// caller asked for auto" must not be the same thing: Validate refuses an
// empty mode, so a caller that forgot the field is told, not silently
// connected somewhere.
type Connect_params struct {
	Mode     string `json:"mode"`               // Mode_auto or Mode_manual
	Node_id  string `json:"node_id,omitempty"`  // read only in Mode_manual
	Protocol string `json:"protocol,omitempty"` // read only in Mode_manual
}

// Validate refuses a mode that is neither constant, and in Mode_manual,
// refuses an empty Node_id or an empty Protocol. In Mode_auto, a Node_id or
// Protocol sent anyway is not an error -- it is ignored, exactly as
// connection.Connect_request ignores them, because the control plane is the
// one choosing.
func (p Connect_params) Validate() error {
	switch p.Mode {
	case Mode_auto:
		return nil
	case Mode_manual:
		switch {
		case p.Node_id == "":
			return errors.New("ipc: connect params: node_id is required in manual mode")
		case p.Protocol == "":
			return errors.New("ipc: connect params: protocol is required in manual mode")
		}
		return nil
	default:
		return fmt.Errorf("ipc: connect params: mode %q is neither %q nor %q", p.Mode, Mode_auto, Mode_manual)
	}
}

// Sign_in_params is the only secret this protocol ever carries, and it
// travels exactly once.
type Sign_in_params struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// String never prints the password, for the same reason
// wireguard.Key.String does not print key material: one careless %v on a
// Sign_in_params in a log line later must not be able to leak a credential.
//
// There is deliberately no MarshalText or MarshalJSON here -- either would
// let a marshaller reach the password by exactly the accident this method
// exists to prevent. encoding/json still marshals Sign_in_params normally,
// field by field, which is how it is actually sent over the wire.
func (p Sign_in_params) String() string {
	return fmt.Sprintf("ipc.Sign_in_params{Username: %q, Password: (not shown)}", p.Username)
}

// Validate refuses an empty username or an empty password.
func (p Sign_in_params) Validate() error {
	switch {
	case p.Username == "":
		return errors.New("ipc: sign_in params: username is required")
	case p.Password == "":
		return errors.New("ipc: sign_in params: password is required")
	}
	return nil
}

// Sign_in_result never carries the device token. The interface passes a
// password through once and never sees the token that comes back (design
// section 2); a device_token field here would undo that in one line.
type Sign_in_result struct {
	Device_id string `json:"device_id"`
}

// Status_result answers Command_status.
//
// Since is a pointer on purpose. There is no connection at all in
// disconnected, and a zero time.Time marshals as "0001-01-01T00:00:00Z",
// which an interface would happily render as a date in the year one. The
// project rule is that a number shown to a person is either a real
// measurement or absent; absent here means the field is not in the JSON at
// all, which is why Since is a pointer with omitempty rather than a bare
// time.Time -- encoding/json does not consider a zero time.Time empty, only
// a nil pointer.
type Status_result struct {
	Statistics       *statistics.Snapshot `json:"statistics,omitempty"`
	Statistics_error bool                 `json:"statistics_error,omitempty"`
	State            string               `json:"state"`
	Signed_in        bool                 `json:"signed_in"`

	// Username and Device_name say who is signed in and as which device,
	// for the account menu; present only while signed in, and Username
	// only when the sign-in recorded it.
	Username    string `json:"username,omitempty"`
	Device_name string `json:"device_name,omitempty"`

	Node_id    string     `json:"node_id,omitempty"`
	Protocol   string     `json:"protocol,omitempty"`
	Session_id string     `json:"session_id,omitempty"`
	Since      *time.Time `json:"since,omitempty"`

	// Tunnel is what the live tunnel was built to carry: vpn, antizapret
	// or games. It is what the session was connected with, not the stored
	// wish, and is present under the same condition as Node_id.
	Tunnel string `json:"tunnel,omitempty"`

	// Node_name, Country and City describe the node for a person, and are
	// present only when the control plane named this node -- see
	// connection.Session.
	Node_name string `json:"node_name,omitempty"`
	Country   string `json:"country,omitempty"`
	City      string `json:"city,omitempty"`

	// Rtt_ms and Loss_percent come from the last successful probe of this
	// session, taken at Measured_at. All three are pointers for the same
	// reason as Since: a session not yet measured has no numbers, and a
	// zero would read as a perfect connection.
	Rtt_ms           *float64   `json:"rtt_ms,omitempty"`
	Loss_percent     *float64   `json:"loss_percent,omitempty"`
	Measured_at      *time.Time `json:"measured_at,omitempty"`
	Degraded_latency bool       `json:"degraded_latency,omitempty"`
	Degraded_probes  bool       `json:"degraded_probes,omitempty"`
	// Degraded_resent: probe connections got through only after TCP sent
	// their SYN again; Resent_percent is how many of them did.
	Degraded_resent bool     `json:"degraded_resent,omitempty"`
	Resent_percent  *float64 `json:"resent_percent,omitempty"`
	// Udp_rtt_ms and Udp_loss_percent come from the DNS queries the last
	// probe sent over UDP; Degraded_udp says too many went unanswered.
	// Udp_loss_percent rides with the sample, so the reason has its
	// number whenever it is shown.
	Udp_rtt_ms       *float64 `json:"udp_rtt_ms,omitempty"`
	Udp_loss_percent *float64 `json:"udp_loss_percent,omitempty"`
	Degraded_udp     bool     `json:"degraded_udp,omitempty"`
	Baseline_rtt_ms  *float64 `json:"baseline_rtt_ms,omitempty"`

	// Reason and Goals are how the control plane chose this node, under
	// the same condition as Node_name.
	Reason string         `json:"reason,omitempty"`
	Goals  []Goal_outcome `json:"goals,omitempty"`

	// Failures is present when the connection has kept failing lately in a
	// way no network change or disconnect explains; see Failure_signal.
	Failures *Failure_signal `json:"failures,omitempty"`
}

// Version_result answers Command_version. Every field is omitempty for the
// same reason: internal/buildinfo leaves a value empty when the build did
// not supply it, and a version the build did not supply is not printed --
// never substituted with "unknown" or "dev".
type Version_result struct {
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Built   string `json:"built,omitempty"`

	// Control_plane is the control plane's current base URL, which a
	// signed manifest may have changed since the build; the window opens
	// the account portal there.
	Control_plane string `json:"control_plane,omitempty"`
}

// Error codes a Response carries in its Error_body.
const (
	Error_unknown_command = "unknown_command"
	Error_bad_request     = "bad_request"   // malformed JSON, or params that failed Validate
	Error_busy            = "busy"          // connection.Err_busy
	Error_not_signed_in   = "not_signed_in" // no device token stored
	Error_unauthorized    = "unauthorized"  // the control plane rejected the credentials
	Error_internal        = "internal"      // anything else

	Error_tunnel_up            = "tunnel_up"            // the analysis needs the tunnel down, or the connection moved while it ran
	Error_server_unavailable   = "server_unavailable"   // the chosen server cannot take a connection now
	Error_no_server_in_country = "no_server_in_country" // no server in the chosen country can
	Error_offer_changed        = "offer_changed"        // the match server offered is not the one answered about
)

// The sentinel errors a Service implementation returns, so the server can
// map them to the codes above by errors.Is without importing connection or
// api -- an implementation may wrap them with context and the mapping still
// works.
//
// Everything unrecognised becomes Error_internal, and its Message is the
// error's own text. That is a deliberate departure from the usual rule
// about not leaking error detail across a boundary: the boundary here is a
// named pipe between two processes on the same machine, already
// access-controlled by its DACL, and a generic "internal error" would make
// a fault the person reading it could diagnose locally into one they
// cannot diagnose at all.
var (
	Err_not_signed_in = errors.New("ipc: no credentials are stored")
	Err_unauthorized  = errors.New("ipc: the control plane rejected these credentials")
	Err_busy          = errors.New("ipc: busy with another operation")

	Err_tunnel_up = errors.New("ipc: a connection is up or was changing, so a direct measurement would measure the tunnel")

	Err_server_unavailable   = errors.New("ipc: the chosen server cannot take a connection right now")
	Err_no_server_in_country = errors.New("ipc: no server in the chosen country can take a connection right now")

	Err_offer_changed = errors.New("ipc: the match server offered has changed")
)

// Max_frame_bytes bounds one line. A caller on the other side of the pipe
// is a local process the DACL already vouched for, not a stranger -- but
// "already vouched for" is not "correct", and a service running as
// LocalSystem must not be able to be made to allocate without limit by a
// user-mode process that got into a loop. 1 MiB is far beyond any real
// frame here. The largest thing this protocol carries is a problem draft: a
// log tail and a diagnostics bundle, each bounded by the service so that the
// two together, escaped for JSON in the worst case, stay well under it.
const Max_frame_bytes = 1 << 20

// Write_timeout bounds one frame write. A client that stops reading must
// not be able to wedge the service behind a full pipe buffer: the write
// fails, the connection is closed, and everything else carries on.
const Write_timeout = 5 * time.Second

// Profile is one of the account's profiles: what a device connects as. One
// profile on two devices at once moves the older one off.
type Profile struct {
	Id    string `json:"id"`
	Label string `json:"label"`
	// Online is present only when the control plane knows: true when some
	// device is connected with this profile now.
	Online *bool `json:"online,omitempty"`
	// Current marks the profile this installation connects with.
	Current bool `json:"current,omitempty"`
}

// Profiles_result answers Command_profiles, newest first.
type Profiles_result struct {
	Profiles []Profile `json:"profiles"`
}

// Set_profile is Command_set_profile's parameter: the profile's id.
type Set_profile struct {
	Id string `json:"id"`
}

func (p Set_profile) Validate() error {
	if p.Id == "" || len(p.Id) > 64 {
		return fmt.Errorf("ipc: profile id of %d bytes", len(p.Id))
	}
	for _, r := range p.Id {
		if r < 0x21 || r > 0x7e {
			return fmt.Errorf("ipc: profile id %q is not printable ASCII", p.Id)
		}
	}
	return nil
}
