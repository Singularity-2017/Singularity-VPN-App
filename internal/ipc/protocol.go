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
//   - switch -- recovery chooses a different target on its own. A caller
//     that can name a node and force a move is a bigger surface for no MVP
//     feature: manual choice of a specific node is not in v1 (design
//     section 8).
//   - settings -- Connect_on_start is real and lives in state.State, but
//     nothing reads it until the installer and the interface exist. A
//     command whose effect nothing implements is a promise, not a feature.
const (
	Command_status     = "status"
	Command_connect    = "connect"
	Command_disconnect = "disconnect"
	Command_sign_in    = "sign_in"
	Command_sign_out   = "sign_out"
	Command_subscribe  = "subscribe"
	Command_version    = "version"
)

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
