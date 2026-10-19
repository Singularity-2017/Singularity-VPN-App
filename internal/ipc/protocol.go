package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
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

	Command_analyze  = "analyze"
	Command_analysis = "analysis"
)

// Strategy is what the connection is for -- see state.Strategy, which it
// mirrors on the wire. The zero value asks for nothing in particular.
type Strategy struct {
	Anonymity string `json:"anonymity,omitempty"` // "" off, or light, standard, high
	Bypass    bool   `json:"bypass,omitempty"`
	Country   string `json:"country,omitempty"` // ISO 3166-1 alpha-2, "" for anywhere
	Route     bool   `json:"route,omitempty"`
	Node_id   string `json:"node_id,omitempty"` // a server picked by hand, "" for automatic
}

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
	return nil
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
// Present is false when the service has none -- it is kept in memory only,
// so a restart forgets it -- and Result is then absent rather than empty.
type Last_analysis struct {
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
	State      string     `json:"state"`
	Signed_in  bool       `json:"signed_in"`
	Node_id    string     `json:"node_id,omitempty"`
	Protocol   string     `json:"protocol,omitempty"`
	Session_id string     `json:"session_id,omitempty"`
	Since      *time.Time `json:"since,omitempty"`

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
	Rtt_ms       *float64   `json:"rtt_ms,omitempty"`
	Loss_percent *float64   `json:"loss_percent,omitempty"`
	Measured_at  *time.Time `json:"measured_at,omitempty"`

	// Reason and Goals are how the control plane chose this node, under
	// the same condition as Node_name.
	Reason string         `json:"reason,omitempty"`
	Goals  []Goal_outcome `json:"goals,omitempty"`
}

// Version_result answers Command_version. Every field is omitempty for the
// same reason: internal/buildinfo leaves a value empty when the build did
// not supply it, and a version the build did not supply is not printed --
// never substituted with "unknown" or "dev".
type Version_result struct {
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Built   string `json:"built,omitempty"`
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
)

// Max_frame_bytes bounds one line. A caller on the other side of the pipe
// is a local process the DACL already vouched for, not a stranger -- but
// "already vouched for" is not "correct", and a service running as
// LocalSystem must not be able to be made to allocate without limit by a
// user-mode process that got into a loop. 1 MiB is far beyond any real
// frame here; the largest thing this protocol carries is a status line.
const Max_frame_bytes = 1 << 20

// Write_timeout bounds one frame write. A client that stops reading must
// not be able to wedge the service behind a full pipe buffer: the write
// fails, the connection is closed, and everything else carries on.
const Write_timeout = 5 * time.Second
