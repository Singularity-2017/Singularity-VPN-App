package api

import (
	"errors"
	"fmt"
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
