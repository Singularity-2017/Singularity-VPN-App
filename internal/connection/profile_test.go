package connection

import (
	"context"
	"errors"
	"slices"
	"testing"

	"singularity-vpn-app/internal/api"
)

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }

func Test_a_first_connect_takes_the_free_pc_profile_instead_of_creating_one(t *testing.T) {
	env := new_test_env(t)
	env.control_plane.profiles = []api.Account_profile{
		{Id: "phone", Label: "Phone"},
		{Id: "pc-busy", Label: "PC", Online: yes()},
		{Id: "pc-free", Label: "pc 2", Online: no()},
	}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.control_plane.create_profile_calls != 0 {
		t.Fatalf("Create_profile calls = %d, want none: a free PC profile exists", env.control_plane.create_profile_calls)
	}
	if env.store.state.Client_id != "pc-free" || !slices.Contains(env.control_plane.fetch_clients, "pc-free") {
		t.Fatalf("stored %q, fetched for %v; want pc-free", env.store.state.Client_id, env.control_plane.fetch_clients)
	}
}

func Test_a_pc_seen_offline_is_taken_before_one_not_polled(t *testing.T) {
	got, ok := pick_free_pc([]api.Account_profile{{Id: "unknown", Label: "PC"}, {Id: "offline", Label: "PC 2", Online: no()}})
	if !ok || got != "offline" {
		t.Fatalf("pick_free_pc = %q, want the one seen offline", got)
	}
}

func Test_a_pc_whose_config_cannot_be_had_is_not_kept(t *testing.T) {
	env := new_test_env(t)
	env.control_plane.profiles = []api.Account_profile{{Id: "pc", Label: "PC", Online: no()}}
	env.control_plane.fetch_config_err = errors.New("node down")
	manager := env.build()
	_ = manager.Connect(context.Background(), Connect_request{Mode: Mode_auto})
	if env.store.state.Client_id == "pc" {
		t.Fatal("the PC profile was kept although its config never came")
	}
}

func Test_a_new_profile_is_named_the_first_free_pc_label(t *testing.T) {
	env := new_test_env(t)
	env.control_plane.profiles = []api.Account_profile{{Id: "other", Label: "PC", Online: yes()}}
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.control_plane.create_profile_calls != 1 || !slices.Equal(env.control_plane.labels, []string{"client-1=PC 2"}) {
		t.Fatalf("created %d, labelled %v; want one, named PC 2", env.control_plane.create_profile_calls, env.control_plane.labels)
	}
}

func Test_a_profile_list_that_fails_still_provisions(t *testing.T) {
	env := new_test_env(t)
	env.control_plane.profiles_err = errors.New("down")
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if env.control_plane.create_profile_calls != 1 {
		t.Fatalf("Create_profile calls = %d, want 1", env.control_plane.create_profile_calls)
	}
}

func Test_pc_labels(t *testing.T) {
	for label, want := range map[string]bool{"PC": true, " pc ": true, "PC 2": true, "PC 1": false, "PC2": false, "PC +2": false, "PC 02": false, "PC x": false, "Laptop": false} {
		if got := is_pc(label); got != want {
			t.Errorf("is_pc(%q) = %v, want %v", label, got, want)
		}
	}
	if got := next_pc_label([]api.Account_profile{{Label: "PC"}, {Label: "PC 3"}}); got != "PC 2" {
		t.Errorf("next_pc_label = %q, want PC 2", got)
	}
}

func Test_another_profile_while_connected_rebuilds_with_its_config(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "first"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := manager.Set_profile(context.Background(), "second"); err != nil {
		t.Fatalf("Set_profile = %v", err)
	}
	if env.store.state.Client_id != "second" || env.control_plane.fetch_clients[len(env.control_plane.fetch_clients)-1] != "second" {
		t.Fatalf("stored %q, fetched for %v; want the second profile's config", env.store.state.Client_id, env.control_plane.fetch_clients)
	}
	if state := manager.State(); state != State_connected {
		t.Fatalf("state = %s, want connected", state)
	}
}
