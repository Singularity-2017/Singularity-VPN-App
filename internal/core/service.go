package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/buildinfo"
	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/security"
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

	result := ipc.Status_result{
		State:     string(a.manager.State()),
		Signed_in: signed_in,
	}

	session, ok := a.manager.Session()
	if !ok {
		return result, nil
	}
	result.Node_id = session.Node_id
	result.Protocol = session.Protocol
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

	// A sample without a round trip -- a prober that confirmed reachability
	// without timing it -- reports nothing rather than a perfect zero.
	if sample, ok := a.manager.Last_sample(); ok && sample.Rtt > 0 {
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
// caller decides what to do with it.
func (a *Application) signed_in() (bool, error) {
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
