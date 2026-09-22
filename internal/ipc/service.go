package ipc

import (
	"context"

	"singularity-vpn-app/internal/events"
)

// Service is everything the pipe can ask the application to do: the whole
// privileged surface of docs/WINDOWS_CLIENT.md section 45, as an interface,
// so that this package is testable without a control plane, a tunnel or a
// privilege. internal/core implements it in stage 4B.
//
// This package imports internal/events and nothing else of this project's
// packages -- no connection, no api, no core. That is what keeps the
// dependency arrow pointing one way, from the application into the
// transport and never back.
type Service interface {
	Status(ctx context.Context) (Status_result, error)
	Connect(ctx context.Context, params Connect_params) error
	Disconnect(ctx context.Context) error
	Sign_in(ctx context.Context, params Sign_in_params) (Sign_in_result, error)
	Sign_out(ctx context.Context) error

	// Subscribe returns exactly what events.Bus.Subscribe returns, so an
	// implementation is one line.
	Subscribe() (<-chan events.Event, func())

	Version(ctx context.Context) (Version_result, error)
}
