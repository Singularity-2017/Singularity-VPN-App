package core

import (
	"context"
	"errors"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/security"
)

func (a *Application) Account(ctx context.Context) (ipc.Account_data, error) {
	reader, ok := a.control_plane.(interface {
		Account(context.Context, string) (ipc.Account_data, error)
	})
	if !ok {
		return ipc.Account_data{}, errors.New("core: this control plane does not read account data")
	}
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return ipc.Account_data{}, map_error(err)
	}
	result, err := reader.Account(ctx, token)
	if err != nil {
		return result, map_error(err)
	}
	device_id, err := a.secrets.Get(security.Key_device_id)
	if err != nil && !errors.Is(err, security.Err_not_found) {
		return ipc.Account_data{}, err
	}
	result.Devices = append([]ipc.Account_device(nil), result.Devices...)
	for i := range result.Devices {
		result.Devices[i].Current = len(device_id) > 0 && result.Devices[i].Device_id == string(device_id)
	}
	return result, nil
}

// The control plane reader is found by shape at run time; pin that shape to
// the real client so a drifting signature fails the build, not the window.
var _ ipc.Spending_service = (*Application)(nil)
var _ interface {
	Spending_page(context.Context, string, string) (ipc.Account_spending, error)
} = (*api.Client)(nil)

func (a *Application) Spending_page(ctx context.Context, params ipc.Spending_page_params) (ipc.Account_spending, error) {
	if err := params.Validate(); err != nil {
		return ipc.Account_spending{}, err
	}
	reader, ok := a.control_plane.(interface {
		Spending_page(context.Context, string, string) (ipc.Account_spending, error)
	})
	if !ok {
		return ipc.Account_spending{}, errors.New("core: this control plane does not read spending")
	}
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return ipc.Account_spending{}, map_error(err)
	}
	result, err := reader.Spending_page(ctx, token, params.Cursor)
	return result, map_error(err)
}

func (a *Application) Feedback(ctx context.Context, params ipc.Feedback_params) (ipc.Feedback_result, error) {
	if err := params.Validate(); err != nil {
		return ipc.Feedback_result{}, err
	}
	writer, ok := a.control_plane.(interface {
		Feedback(context.Context, string, ipc.Feedback_params) (ipc.Feedback_result, error)
	})
	if !ok {
		return ipc.Feedback_result{}, errors.New("core: this control plane does not submit feedback")
	}
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return ipc.Feedback_result{}, map_error(err)
	}
	result, err := writer.Feedback(ctx, token, params)
	return result, map_error(err)
}
