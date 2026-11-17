package core

import (
	"context"
	"errors"
	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/ipc"
)

type vless_control_plane interface {
	Vless_options(context.Context, string, string) (api.Vless_options, error)
	Fetch_vless(context.Context, string, string, string) (api.Vpn_config, error)
}

func (a *Application) Vless_options(ctx context.Context, profile ipc.Set_profile) (ipc.Vless_options_result, error) {
	if err := profile.Validate(); err != nil {
		return ipc.Vless_options_result{}, err
	}
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return ipc.Vless_options_result{}, map_error(err)
	}
	plane, ok := a.control_plane.(vless_control_plane)
	if !ok {
		return ipc.Vless_options_result{}, errors.New("core: VLESS export unavailable")
	}
	options, err := plane.Vless_options(ctx, token, profile.Id)
	if err != nil {
		return ipc.Vless_options_result{}, map_error(err)
	}
	result := ipc.Vless_options_result{Nodes: []ipc.Vless_node{}}
	for _, node := range options.Nodes {
		result.Nodes = append(result.Nodes, ipc.Vless_node{Node_id: node.Node_id, Vless_available: node.Vless_available, Reason: node.Reason})
	}
	return result, nil
}

func (a *Application) Export_vless(ctx context.Context, params ipc.Export_vless) (ipc.Vless_result, error) {
	if err := params.Validate(); err != nil {
		return ipc.Vless_result{}, err
	}
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return ipc.Vless_result{}, map_error(err)
	}
	plane, ok := a.control_plane.(vless_control_plane)
	if !ok {
		return ipc.Vless_result{}, errors.New("core: VLESS export unavailable")
	}
	config, err := plane.Fetch_vless(ctx, token, params.Client_id, params.Node_id)
	if err != nil {
		return ipc.Vless_result{}, map_error(err)
	}
	return ipc.Vless_result{Config: config.Config}, nil
}
