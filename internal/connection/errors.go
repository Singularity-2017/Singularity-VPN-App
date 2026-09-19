package connection

import (
	"context"
	"errors"
	"fmt"

	"singularity-vpn-app/internal/api"
)

// Fault_kind names the kind of thing that went wrong, so the interface can
// react to it -- show a login prompt, a "server unreachable" banner, a
// permission dialog -- without parsing an error string. CLIENT_CORE.md
// section 32.
type Fault_kind string

const (
	Fault_authentication            Fault_kind = "authentication"
	Fault_control_plane_unavailable Fault_kind = "control_plane_unavailable"
	Fault_recommendation            Fault_kind = "recommendation"
	Fault_configuration             Fault_kind = "configuration"
	Fault_protocol_unavailable      Fault_kind = "protocol_unavailable"
	Fault_protocol_connection       Fault_kind = "protocol_connection"
	Fault_verification              Fault_kind = "verification"
	Fault_platform                  Fault_kind = "platform"
	Fault_permission                Fault_kind = "permission"
	Fault_timeout                   Fault_kind = "timeout"

	// Fault_recovery_exhausted is not in CLIENT_CORE.md section 32, whose
	// list is given as examples rather than as a closed set. It is added
	// because the alternative was calling "every target was tried and none
	// worked" a configuration fault, and a person told to check their
	// configuration when the servers are the problem is being sent to fix
	// something they did not break -- which is the exact failure section 32
	// exists to prevent.
	Fault_recovery_exhausted Fault_kind = "recovery_exhausted"
)

// Fault is what Connect, Switch and Disconnect report to the interface: a
// Kind a person's screen can react to, Op naming the operation for a log
// line ("connect", "verify", "switch"), and the underlying error for
// whoever reads that log.
//
// A Fault must never carry config bytes. api.Vpn_config.Config,
// api.Profile.Config and protocols.Connection_config.Data are the tunnel's
// key material, and nothing in this package ever puts them into an Op, an
// Err or anything else a Fault prints -- the classify_* functions below
// build every Fault from the error a call returned, never from the request
// that produced it.
type Fault struct {
	Kind Fault_kind
	Op   string
	Err  error
}

func (f *Fault) Error() string {
	if f.Op != "" {
		return fmt.Sprintf("connection: %s: %s: %v", f.Op, f.Kind, f.Err)
	}
	return fmt.Sprintf("connection: %s: %v", f.Kind, f.Err)
}

// Unwrap lets errors.Is and errors.As see through a Fault to whatever
// produced it.
func (f *Fault) Unwrap() error {
	return f.Err
}

// Kind_of reports the Fault_kind of err, if err is or wraps a *Fault. A
// caller that gets false back has an error this package never classified --
// a programming mistake, not something to show a person as a kind of
// failure.
func Kind_of(err error) (Fault_kind, bool) {
	var fault *Fault
	if errors.As(err, &fault) {
		return fault.Kind, true
	}
	return "", false
}

// classify_control_plane_error turns whatever Recommend, Create_profile or
// Fetch_config handed back into the Fault the interface can act on. op
// identifies which call was in flight, because the same *api.Error means
// something different from Recommend (the server could not choose) than
// from Create_profile or Fetch_config (the server could not provision or
// hand back a config) -- only api.Code_unauthorized reads the same
// everywhere.
//
// A transport failure and api.Err_incomplete_response land on the same
// Fault_control_plane_unavailable: from the person's side, a server that
// answers wrongly and a server that does not answer are the same unusable
// server, and neither is an *api.Error.
func classify_control_plane_error(op string, err error) *Fault {
	if api.Is_code(err, api.Code_unauthorized) {
		return &Fault{Kind: Fault_authentication, Op: op, Err: err}
	}
	var api_err *api.Error
	if errors.As(err, &api_err) {
		if op == "recommend" {
			return &Fault{Kind: Fault_recommendation, Op: op, Err: err}
		}
		return &Fault{Kind: Fault_configuration, Op: op, Err: err}
	}
	return &Fault{Kind: Fault_control_plane_unavailable, Op: op, Err: err}
}

// classify_protocol_lookup_error turns a failure of protocols.Manager.Get
// into a Fault. The only documented reason Get fails is
// protocols.Err_unknown_protocol, but any error from it means the same
// thing to a person: the protocol they need is not available on this
// installation.
func classify_protocol_lookup_error(op string, err error) *Fault {
	return &Fault{Kind: Fault_protocol_unavailable, Op: op, Err: err}
}

// classify_connect_error turns a failure of Protocol.Connect into a Fault.
// context.DeadlineExceeded means Protocol_timeout was reached, which
// CLIENT_AUTO_SWITCH.md section 5.1 calls a hard failure in its own right
// and worth telling apart from an adapter that failed immediately.
func classify_connect_error(op string, err error) *Fault {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Fault{Kind: Fault_timeout, Op: op, Err: err}
	}
	return &Fault{Kind: Fault_protocol_connection, Op: op, Err: err}
}

// classify_verify_error turns a failure of diagnostics.Verify into a Fault.
// diagnostics.Err_tunnel_down, diagnostics.Err_no_internet and a raw probe
// error are all "verification could not confirm a tunnel" from here -- one
// Fault_kind, with the underlying error still saying which one for the log.
func classify_verify_error(op string, err error) *Fault {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Fault{Kind: Fault_timeout, Op: op, Err: err}
	}
	return &Fault{Kind: Fault_verification, Op: op, Err: err}
}

// fault_from_recovery_error wraps whatever recovery.Policy.Next_target
// refused with. It exists for the paths where nothing else already has a
// more specific Fault to report -- Tick's own decision to leave a degraded
// or lost connection -- as opposed to a failed establish inside Connect or
// Switch, which already carries the technical reason the attempt failed.
func fault_from_recovery_error(op string, err error) *Fault {
	return &Fault{Kind: Fault_recovery_exhausted, Op: op, Err: err}
}
