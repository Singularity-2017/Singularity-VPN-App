package core

import (
	"context"
	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/ipc"
	"testing"
)

type vless_test_plane struct {
	*fake_control_plane
	calls   int
	token   string
	profile string
	node    string
}

func (f *vless_test_plane) Vless_options(ctx context.Context, token, profile string) (api.Vless_options, error) {
	f.calls++
	f.token = token
	f.profile = profile
	return api.Vless_options{Nodes: []api.Vless_node{{Node_id: "node", Vless_available: true}}}, nil
}
func (f *vless_test_plane) Fetch_vless(ctx context.Context, token, profile, node string) (api.Vpn_config, error) {
	f.calls++
	f.token = token
	f.profile = profile
	f.node = node
	return api.Vpn_config{Config: "vless://test@example.invalid:443"}, nil
}
func Test_vless_uses_service_device_token_and_never_connects_or_adopts_profile(t *testing.T) {
	f := new_fixture(t)
	plane := &vless_test_plane{fake_control_plane: f.control_plane}
	f.app.control_plane = plane
	ctx := context.Background()
	if _, err := f.app.Vless_options(ctx, ipc.Set_profile{Id: "profile"}); err == nil {
		t.Fatal("signed out discovery succeeded")
	}
	if _, err := f.app.Export_vless(ctx, ipc.Export_vless{Client_id: "profile", Node_id: "node"}); err == nil {
		t.Fatal("signed out export succeeded")
	}
	if plane.calls != 0 {
		t.Fatal("signed out request reached backend")
	}
	sign_in(t, f)
	options, err := f.app.Vless_options(ctx, ipc.Set_profile{Id: "profile"})
	if err != nil || len(options.Nodes) != 1 {
		t.Fatalf("discovery: %v", err)
	}
	result, err := f.app.Export_vless(ctx, ipc.Export_vless{Client_id: "profile", Node_id: "node"})
	if err != nil || result.Config == "" {
		t.Fatalf("export: %v", err)
	}
	if plane.token != "device-token" || plane.profile != "profile" || plane.node != "node" {
		t.Fatal("wrong export identity")
	}
	id, _ := f.app.manager.Client_id()
	if id != "" || f.protocol.connect_calls != 0 {
		t.Fatal("export changed the tunnel profile")
	}
}
