package api

import (
	"errors"
	"fmt"
	"strings"
)

// Error is what a call returns when the control plane itself said no -- a
// 4xx or 5xx with an envelope, or a 2xx envelope with "success": false. A
// transport failure (connection refused, TLS, timeout) is never an *Error:
// it comes back wrapped as whatever net/http produced, so a caller can tell
// "the server said no" from "I could not reach the server" with a single
// errors.As.
type Error struct {
	Status  int    // HTTP status
	Code    string // the control plane's error code, empty if the body had none
	Message string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("control plane: %s (%d %s)", e.Message, e.Status, e.Code)
	}
	return fmt.Sprintf("control plane: %s (%d)", e.Message, e.Status)
}

// Named error codes the control plane is documented to send. Compare against
// these, never against the string literal at the call site.
const (
	Code_unauthorized             = "UNAUTHORIZED"
	Code_client_limit_reached     = "CLIENT_LIMIT_REACHED"
	Code_node_unavailable         = "NODE_UNAVAILABLE"
	Code_client_not_found         = "CLIENT_NOT_FOUND"
	Code_invalid_input            = "INVALID_INPUT"
	Code_forbidden                = "FORBIDDEN"
	Code_internal_error           = "INTERNAL_ERROR"
	Code_device_token_cannot_mint = "DEVICE_TOKEN_CANNOT_MINT"
)

// Is_code reports whether err is an *Error carrying the given control-plane
// code, so the state machine in stage 2 does not repeat the errors.As dance
// at every call site.
func Is_code(err error, code string) bool {
	var api_err *Error
	if errors.As(err, &api_err) {
		return api_err.Code == code
	}
	return false
}

// Err_incomplete_response is what a call returns when the control plane's
// answer decoded cleanly but arrived without a value this client acts on. Go
// cannot tell a field the server renamed from a field the server left empty:
// both land as the zero value, and the call would otherwise succeed carrying
// an empty client_id or an empty config. Refusing is the cheaper failure --
// by the time Create_profile's answer is decoded the account has already
// spent one of its profile slots, and a client_id silently lost at that
// moment is a slot nobody can reclaim.
//
// It is deliberately neither an *Error nor a transport failure: the server
// did not say no, and the connection worked. It is also never retried -- a
// second answer would be incomplete in exactly the same way.
var Err_incomplete_response = errors.New("api: the control plane's answer is incomplete")

// required_field names one value a response must carry and says whether it
// arrived.
type required_field struct {
	name    string
	arrived bool
}

// check_required reports every missing field at once rather than the first
// one, because a rename on the server side usually moves more than one field
// and one round trip per discovery is a slow way to find that out.
func check_required(what string, fields ...required_field) error {
	var missing []string
	for _, field := range fields {
		if !field.arrived {
			missing = append(missing, field.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s arrived without %s",
		Err_incomplete_response, what, strings.Join(missing, ", "))
}
