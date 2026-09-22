//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// The pipe itself is go-winio's rather than hand-written CreateNamedPipe.
// This project keeps everything impure thin: overlapped I/O, cancellation
// and listener shutdown on Windows pipes are some three hundred lines that
// cannot be tested from anywhere but a real machine, and go-winio is the
// copy of them Docker and containerd have been running for a decade. What
// is left in this file is the two decisions that are this application's
// own: who may open the pipe, and whom a caller is willing to talk to.

// pipe_security is the pipe's access control list, and it is the whole
// access control story of this protocol: the kernel decides who may open
// the pipe, before a single byte is read. WINDOWS_CLIENT.md 22 asks the
// service to check who is calling; this is that check, made by the part of
// the system that cannot be talked out of it.
//
//	O:BA           owned by Administrators, set explicitly. Left to default,
//	               the owner is whatever the creating token names, and on
//	               a stock Windows install an elevated administrator's token
//	               names the user, not the group -- so the pipe of a service
//	               run from an elevated console would look exactly like a
//	               squatter's to the owner check in Dial. Only a token that
//	               holds Administrators as an enabled group may assign it
//	               as owner: LocalSystem does, an elevated administrator
//	               does, an ordinary process does not, and its attempt to
//	               create a pipe carrying this string fails outright.
//	D:P            a protected DACL -- no inherited entries, so nothing an
//	               administrator once set on a parent object can widen it
//	(A;;GA;;;SY)   LocalSystem: the service itself
//	(A;;GA;;;BA)   Administrators: so the machine's owner can diagnose it
//	(A;;GRGW;;;IU) the interactive user: read and write, and deliberately
//	               not FILE_CREATE_PIPE_INSTANCE -- the user may talk to
//	               the service, never impersonate it
//
// Everybody else is refused by omission. A service account, a scheduled
// task running as a different user, a network logon: none of them match
// IU, and none of them gets in. go-winio also sets
// FILE_PIPE_REJECT_REMOTE_CLIENTS on every instance it creates, so the pipe
// is not reachable over the network whatever this string says.
const pipe_security = `O:BAD:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)`

// Listen creates the service's end of the pipe.
//
// A failure here must be treated as fatal by the caller, and the message
// says why. go-winio opens the first instance with FILE_CREATE, which fails
// when the name already exists -- and the first process to create a pipe
// name owns it. So "cannot create the pipe" may well mean that something
// which is not this service is already answering on this service's name,
// waiting for somebody to type a password at it. Retrying, or falling back
// to another name, would hide exactly that.
func Listen() (net.Listener, error) {
	listener, err := winio.ListenPipe(Pipe_name, &winio.PipeConfig{
		SecurityDescriptor: pipe_security,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"ipc: cannot create %s -- another copy of the service may be running, "+
				"or another process has taken the name and is impersonating it; "+
				"the service will not start while that is unresolved: %w",
			Pipe_name, err)
	}
	return listener, nil
}

// Err_service_not_running is what Dial returns when the pipe does not
// exist at all.
var Err_service_not_running = errors.New("ipc: the Singularity VPN service is not running")

// Err_untrusted_pipe is what Dial returns when the pipe exists but is not
// provably the service's.
var Err_untrusted_pipe = errors.New("ipc: the pipe is not owned by the Singularity VPN service")

// Dial connects to the service and verifies whom it connected to before
// returning the connection -- before a single byte, and a password in
// particular, goes down it.
//
// This exists because of the one asymmetry in the design. The DACL above
// protects the service from the wrong caller; nothing in it protects the
// caller from the wrong service. A user-mode process that created the name
// first would receive the username and password sign-in sends. So the
// caller checks who owns the pipe object: a pipe created by the service
// running as LocalSystem, or by the service run from an elevated console
// for diagnosis, is owned by LocalSystem or by Administrators; a pipe
// created by an ordinary user's process is owned by that user.
//
// The check is made on the handle already connected, not by looking the
// name up first and then opening it. Looking first would leave a gap in
// which the name could change hands between the check and the open; the
// handle cannot change hands.
func Dial(ctx context.Context) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, Pipe_name)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, fmt.Errorf("%w -- start it with `sc start SingularityVPN`, "+
				"or run `svpn-service run` from an elevated console: %w", Err_service_not_running, err)
		}
		return nil, fmt.Errorf("ipc: connecting to %s: %w", Pipe_name, err)
	}

	if err := verify_pipe_owner(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// verify_pipe_owner fails closed: a check that could not be made is not a
// check that passed, and every path out of here that is not the one
// explicit match is a refusal.
func verify_pipe_owner(conn net.Conn) error {
	with_fd, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("%w: the connection exposes no handle to check", Err_untrusted_pipe)
	}
	handle := windows.Handle(with_fd.Fd())

	// The connection was opened for GENERIC_READ, which carries READ_CONTROL,
	// so reading the owner needs no access the caller does not already hold.
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%w: its owner could not be read: %w", Err_untrusted_pipe, err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("%w: it has no readable owner", Err_untrusted_pipe)
	}
	if owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return nil
	}
	return fmt.Errorf("%w: it is owned by %s, which is neither LocalSystem nor Administrators -- "+
		"another program may be impersonating the service; nothing was sent to it", Err_untrusted_pipe, owner)
}
