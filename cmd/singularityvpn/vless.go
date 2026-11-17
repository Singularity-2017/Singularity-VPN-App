//go:build windows

package main

import (
	"context"
	"os"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"singularity-vpn-app/internal/ipc"
)

type Vless_options_view struct {
	Nodes []ipc.Vless_node `json:"nodes"`
	Error *Outcome         `json:"error,omitempty"`
}

func (a *App) Vless_options(client_id string) Vless_options_view {
	view := Vless_options_view{Nodes: []ipc.Vless_node{}}
	outcome := a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		result, err := c.Vless_options(ctx, ipc.Set_profile{Id: client_id})
		if err == nil && result.Nodes != nil {
			view.Nodes = result.Nodes
		}
		return err
	})
	if outcome.Code != "" {
		view.Error = &outcome
	}
	return view
}

// Download_vless keeps the credential out of the WebView and uses a native
// save dialog. Cancelling never generates an export or changes a profile.
func (a *App) Download_vless(client_id, node_id string) Outcome {
	params := ipc.Export_vless{Client_id: client_id, Node_id: node_id}
	if err := params.Validate(); err != nil {
		return Outcome{Code: "invalid", Message: "Invalid profile or server"}
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title: "Сохранить VLESS", DefaultFilename: "singularity-vless.txt",
		Filters: []runtime.FileFilter{{DisplayName: "VLESS (*.txt)", Pattern: "*.txt"}},
	})
	if err != nil {
		return Outcome{Code: "failed", Message: "Could not open the save dialog"}
	}
	if path == "" {
		return Outcome{}
	}
	outcome := a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		result, err := c.Export_vless(ctx, params)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(result.Config+"\n"), 0600); err != nil {
			return &export_save_error{}
		}
		return nil
	})
	if outcome.Code == "" {
		outcome.Message = "saved"
	}
	return outcome
}

type export_save_error struct{}

func (*export_save_error) Error() string { return "Could not save the VLESS file" }
