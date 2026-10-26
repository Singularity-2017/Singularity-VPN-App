package connection

import (
	"context"
	"errors"
	"testing"

	"singularity-vpn-app/internal/events"
)

func Test_profile_change_event_only_after_success(t *testing.T) {
	for _, scenario := range []string{"offline", "connected", "noop", "cancelled", "save_failure", "config_failure"} {
		t.Run(scenario, func(t *testing.T) {
			env := new_test_env(t)
			env.store.state.Client_id = "first"
			manager := env.build()
			if scenario == "connected" || scenario == "config_failure" {
				if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
					t.Fatal(err)
				}
			}
			channel, unsubscribe := env.bus.Subscribe()
			defer unsubscribe()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			target := "second"
			switch scenario {
			case "noop":
				target = "first"
			case "cancelled":
				cancel()
			case "save_failure":
				env.store.save_err = errors.New("cannot save")
			case "config_failure":
				env.control_plane.fetch_config_err = errors.New("cannot fetch")
			}
			err := manager.Set_profile(ctx, target)
			want_success := scenario == "offline" || scenario == "connected"
			want_error := scenario == "cancelled" || scenario == "save_failure" || scenario == "config_failure"
			if (err != nil) != want_error {
				t.Fatalf("Set_profile error = %v", err)
			}
			count, established := 0, 0
			for _, event := range drain_events(channel) {
				if event.Kind == events.Kind_profile_changed {
					count++
					if event.Data != nil || event.Session_id != "" || event.At.IsZero() {
						t.Fatalf("profile event carries identifiers or lacks time: %+v", event)
					}
				}
				if event.Kind == events.Kind_connection_established {
					if count == 0 || event.Session_id == "" {
						t.Fatalf("the rebuilt tunnel is announced before the profile change or without its session: %+v", event)
					}
					established++
				}
			}
			if (count == 1) != want_success || count > 1 {
				t.Fatalf("profile change events = %d, want success %v", count, want_success)
			}
			// A tunnel rebuilt with the new profile is a connection the
			// journal shows; with nothing up there is nothing to announce.
			if want := scenario == "connected"; (established == 1) != want || established > 1 {
				t.Fatalf("connection established events after the change = %d, want %v", established, want)
			}
		})
	}
}

func Test_the_profile_just_left_reads_as_this_machines_trace_for_a_while(t *testing.T) {
	env := new_test_env(t)
	env.store.state.Client_id = "first"
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatal(err)
	}
	if manager.Left_recently("first") {
		t.Fatal("a profile still in use was reported as left")
	}
	if err := manager.Set_profile(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if !manager.Left_recently("first") || manager.Left_recently("second") || manager.Left_recently("") {
		t.Fatal("only the profile just left is this machine's trace")
	}
	env.clock.advance(left_profile_trace)
	if manager.Left_recently("first") {
		t.Fatal("the trace outlived the control plane's own online window")
	}
}
