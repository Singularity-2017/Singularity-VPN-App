package wireguard

import (
	"context"
	"net/netip"
	"singularity-vpn-app/internal/routing"
	"testing"

	"singularity-vpn-app/internal/protocols"
)

func connected_probe_target(t *testing.T, config protocols.Connection_config) string {
	t.Helper()
	router := &fake_router{}
	a, _ := games_adapter(t, router, &fake_network{})
	a.Enable_games(router, probe)
	if got := a.Probe_target(); got != "" {
		t.Fatalf("Probe_target() before connecting = %q, want none", got)
	}
	if err := a.Connect(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	got := a.Probe_target()
	if err := a.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := a.Probe_target(); after != "" {
		t.Fatalf("Probe_target() after disconnecting = %q, want none", after)
	}
	return got
}

func Test_a_split_tunnel_is_probed_through_its_resolver(t *testing.T) {
	got := connected_probe_target(t, protocols.Connection_config{Data: []byte(split_tunnel_config())})
	if got != "10.29.8.1:53" {
		t.Fatalf("Probe_target() = %q, want the resolver on port 53", got)
	}
}

func Test_a_full_tunnel_keeps_the_static_probe_target(t *testing.T) {
	got := connected_probe_target(t, protocols.Connection_config{Data: []byte(full_tunnel_config())})
	if got != "" {
		t.Fatalf("Probe_target() = %q, want none for a full tunnel", got)
	}
}

func Test_a_games_only_tunnel_keeps_the_static_probe_target(t *testing.T) {
	got := connected_probe_target(t, games_config())
	if got != "" {
		t.Fatalf("Probe_target() = %q, want none in games-only mode", got)
	}
}

func Test_full_probe_routes_requires_active_full_machine_plan(t *testing.T) {
	for _, test := range []struct {
		name   string
		config protocols.Connection_config
		want   bool
	}{
		{"full", protocols.Connection_config{Data: []byte(full_tunnel_config())}, true},
		{"split", protocols.Connection_config{Data: []byte(split_tunnel_config())}, false},
		{"games", games_config(), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := &fake_router{}
			a, _ := games_adapter(t, router, &fake_network{})
			a.Enable_games(router, probe)
			if a.Full_probe_routes() {
				t.Fatal("inactive adapter eligible")
			}
			if err := a.Connect(context.Background(), test.config); err != nil {
				t.Fatal(err)
			}
			if got := a.Full_probe_routes(); got != test.want {
				t.Fatalf("eligible=%v", got)
			}
			if err := a.Disconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			if a.Full_probe_routes() {
				t.Fatal("stale eligibility after disconnect")
			}
		})
	}
}
func Test_full_probe_plan_checks_both_routes_and_endpoint_exclusions(t *testing.T) {
	full := routing.Plan{Full_tunnel: true, Routes: []routing.Route{{Destination: netip.MustParsePrefix("0.0.0.0/1"), Through_tunnel: true}, {Destination: netip.MustParsePrefix("128.0.0.0/1"), Through_tunnel: true}}}
	if !full_probe_plan(full) {
		t.Fatal("full plan not eligible")
	}
	for _, target := range []string{"1.1.1.1/32", "8.8.8.8/32"} {
		plan := full
		plan.Routes = append(append([]routing.Route{}, full.Routes...), routing.Route{Destination: netip.MustParsePrefix(target), Through_tunnel: false})
		if full_probe_plan(plan) {
			t.Fatalf("bypass %s eligible", target)
		}
	}
	full.Full_tunnel = false
	if full_probe_plan(full) {
		t.Fatal("split plan eligible even though target routes exist")
	}
}
