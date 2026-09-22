package core

import (
	"errors"
	"fmt"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/security"
)

// map_error translates an error from this application's own packages into
// the sentinel internal/ipc turns into a code on the wire. Everything it
// does not recognise is returned unchanged, and the server renders it as
// Error_internal with the error's own text -- which is the right outcome
// for a pipe between two processes on the same machine.
//
// The order of the checks is the part worth reading twice. A missing device
// token reaches here as a connection.Fault of kind Fault_authentication,
// because that is how connection.Manager.Connect classifies anything its
// Token_source refuses -- so checking Fault_authentication first would
// report a machine that has never been signed in as one whose password was
// rejected. The interface acts on that difference: not_signed_in means show
// a sign-in form, unauthorized means tell the person their password is
// wrong. Looking for security.Err_not_found first is what keeps the two
// apart, and it works because connection.Fault unwraps to the error it was
// built from.
func map_error(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, security.Err_not_found):
		return fmt.Errorf("%w: %w", ipc.Err_not_signed_in, err)
	case errors.Is(err, connection.Err_busy):
		return fmt.Errorf("%w: %w", ipc.Err_busy, err)
	case api.Is_code(err, api.Code_unauthorized):
		return fmt.Errorf("%w: %w", ipc.Err_unauthorized, err)
	}

	// Last, because the two checks above are more specific readings of
	// errors that also arrive wearing this kind.
	if kind, ok := connection.Kind_of(err); ok && kind == connection.Fault_authentication {
		return fmt.Errorf("%w: %w", ipc.Err_unauthorized, err)
	}
	return err
}
