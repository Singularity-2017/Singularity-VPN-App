//go:build windows && payload

package main

import "testing"

// Exercise the exact bytes embedded in setup without installing or starting it.
func Test_embedded_offline_xray_payload_matches_compiled_pin(t *testing.T) {
	files, err := payload_contents(payload_source())
	if err != nil {
		t.Fatal(err)
	}
	if err := verify_xray_payload(files); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"xray/xray.exe", "xray/wintun.dll", "xray/geoip.dat", "xray/geosite.dat", "xray/LICENSE", "xray/LICENSE-Wintun"} {
		original := files[name]
		files[name] = append([]byte("tampered"), original...)
		if err := verify_xray_payload(files); err == nil {
			t.Fatalf("accepted changed %s", name)
		}
		files[name] = original
	}
}
