package ipc

import (
	"context"
	"testing"
)

type vless_fake_service struct {
	*fake_service
	options_calls int
	export_calls  int
}

func (f *vless_fake_service) Vless_options(ctx context.Context, p Set_profile) (Vless_options_result, error) {
	f.options_calls++
	return Vless_options_result{Nodes: []Vless_node{{Node_id: p.Id, Vless_available: true}}}, nil
}
func (f *vless_fake_service) Export_vless(ctx context.Context, p Export_vless) (Vless_result, error) {
	f.export_calls++
	return Vless_result{Config: p.Client_id + ":" + p.Node_id}, nil
}

func Test_vless_commands_round_trip_and_reject_invalid_ids(t *testing.T) {
	service := &vless_fake_service{fake_service: new_fake_service()}
	client, _ := serve_pair(t, service)
	options, err := client.Vless_options(context.Background(), Set_profile{Id: "profile"})
	if err != nil || len(options.Nodes) != 1 || !options.Nodes[0].Vless_available {
		t.Fatalf("options failed: %v", err)
	}
	result, err := client.Export_vless(context.Background(), Export_vless{Client_id: "profile", Node_id: "node"})
	if err != nil || result.Config != "profile:node" {
		t.Fatalf("export failed: %v", err)
	}
	if _, err := client.Export_vless(context.Background(), Export_vless{Client_id: "profile"}); err == nil {
		t.Fatal("empty node accepted")
	}
	if _, err := client.Vless_options(context.Background(), Set_profile{}); err == nil {
		t.Fatal("empty profile accepted")
	}
	if service.options_calls != 1 || service.export_calls != 1 {
		t.Fatal("invalid requests reached service")
	}
}

func Test_old_service_refuses_vless_without_breaking_profiles(t *testing.T) {
	client, _ := serve_pair(t, new_fake_service())
	if _, err := client.Vless_options(context.Background(), Set_profile{Id: "profile"}); err == nil {
		t.Fatal("old service advertised export")
	}
	if _, err := client.Profiles(context.Background()); err != nil {
		t.Fatal(err)
	}
}
