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

	Strategy(ctx context.Context) (Strategy, error)
	Set_strategy(ctx context.Context, strategy Strategy) error
	Servers(ctx context.Context) (Servers_result, error)
	Ping_servers(ctx context.Context) (Ping_servers_result, error)

	Games(ctx context.Context) (Games, error)
	Set_games(ctx context.Context, games Games) error
	Answer_match_server(ctx context.Context, answer Match_server_answer) error

	Tunnel_mode(ctx context.Context) (Tunnel_mode, error)
	Set_tunnel_mode(ctx context.Context, mode Tunnel_mode) error
	Profiles(ctx context.Context) (Profiles_result, error)
	Set_profile(ctx context.Context, profile Set_profile) error

	Analyze(ctx context.Context) (Analysis_result, error)
	Analysis(ctx context.Context) (Last_analysis, error)

	Journal(ctx context.Context) (Journal_result, error)
}
