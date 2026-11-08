package core

import (
	"context"
	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/ipc"
	"testing"
)

func Test_vless_strategy_connects_and_restores_through_normal_core(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "connect_restore", true: "handover"}[live], func(t *testing.T) {
			native := &fake_protocol{name: "vless"}
			f := new_fixture(t, native)
			sign_in(t, f)
			ctx := context.Background()

			f.control_plane.servers = []api.Server{{Node_id: "node-1", Country: "DE", Available: true, Vless_available: true}}
			if live {
				connect(t, f)
			}
			local, _ := f.store.Load()
			local.Tunnel_mode = "antizapret"
			local.Games = []string{"pubg"}
			local.Game_region = "eu"
			f.store.Save(local)
			f.control_plane.config = api.Vpn_config{Protocol: "vless", Mode: "vpn", Config: "synthetic-native-config"}
			f.control_plane.profile = api.Profile{Client_id: "native-profile", Node_id: "node-1", Protocol: "vless", Config: "synthetic-native-config"}
			if err := f.app.Set_strategy(ctx, ipc.Strategy{Protocol: "vless", Node_id: "node-1", Pinned: true}); err != nil {
				t.Fatal(err)
			}
			if !live {
				if err := f.app.Connect(ctx, ipc.Connect_params{Mode: "auto"}); err != nil {
					t.Fatal(err)
				}
			}
			got, _ := f.app.Status(ctx)
			if got.Protocol != "vless" || got.Tunnel != "vpn" || native.connect_calls != 1 {
				t.Fatalf("native status: %+v calls %d", got, native.connect_calls)
			}
			local, _ = f.store.Load()
			if local.Tunnel_mode != "vpn" || len(local.Games) > 0 || local.Game_region != "" {
				t.Fatal("VLESS did not atomically clear split routing")
			}
			if err := f.app.Disconnect(ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.app.Connect(ctx, ipc.Connect_params{Mode: "auto"}); err != nil {
				t.Fatal(err)
			}
			if native.connect_calls != 2 {
				t.Fatal("restore did not use native protocol")
			}
		})
	}
}

func Test_vless_missing_capability_fails_before_replacing_live_tunnel(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	connect(t, f)
	if err := f.app.Set_strategy(context.Background(), ipc.Strategy{Protocol: "vless"}); err == nil {
		t.Fatal("unknown capability accepted")
	}
	if f.protocol.disconnects() != 0 {
		t.Fatal("working tunnel torn down for unavailable protocol")
	}

}
