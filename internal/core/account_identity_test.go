package core

import (
	"context"
	"encoding/json"
	"testing"

	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/security"
)

type identity_control_plane struct {
	*fake_control_plane
	devices []ipc.Account_device
}

func (f *identity_control_plane) Account(context.Context, string) (ipc.Account_data, error) {
	return ipc.Account_data{Devices: f.devices}, nil
}

func Test_account_current_device_uses_local_identity_not_name(t *testing.T) {
	for _, device_id := range []string{"device-1", "", "not-listed", "missing"} {
		t.Run(device_id, func(t *testing.T) {
			f := new_fixture(t)
			sign_in(t, f)
			if err := f.secrets.Set(security.Key_device_id, []byte(device_id)); err != nil {
				t.Fatal(err)
			}
			if device_id == "missing" {
				if err := f.secrets.Delete(security.Key_device_id); err != nil {
					t.Fatal(err)
				}
			}
			plane := &identity_control_plane{fake_control_plane: f.control_plane, devices: []ipc.Account_device{
				{Device_id: "device-2", Name: "test machine", Current: true},
				{Device_id: "device-1", Name: "test machine"},
				{Name: "test machine", Current: true},
			}}
			f.app.control_plane = plane
			result, err := f.app.Account(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for i, device := range result.Devices {
				if want := i == 1 && device_id == "device-1"; device.Current != want {
					t.Fatalf("device %d current = %v, want %v", i, device.Current, want)
				}
				encoded, err := json.Marshal(device)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				if current, ok := fields["current"].(bool); !ok || current != device.Current {
					t.Fatalf("current JSON field missing or incorrect: %s", encoded)
				}
			}
			if !plane.devices[0].Current || plane.devices[1].Current {
				t.Fatal("account marking mutated the upstream response")
			}
		})
	}
}
