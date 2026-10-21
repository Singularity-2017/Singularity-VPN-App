package main

import (
	"strings"
	"testing"
	"testing/fstest"
)

func full_payload() fstest.MapFS {
	fs := fstest.MapFS{}
	for _, name := range payload_files {
		fs[name] = &fstest.MapFile{Data: []byte("content of " + name)}
	}
	for _, name := range []string{"openvpn/openvpn.exe", "openvpn/LICENSE", "openvpn/libssl-3-x64.dll"} {
		fs[name] = &fstest.MapFile{Data: []byte("content of " + name)}
	}
	return fs
}

func Test_the_whole_openvpn_directory_is_installed_after_the_fixed_files(t *testing.T) {
	got, err := payload_contents(full_payload())
	if err != nil {
		t.Fatalf("payload_contents = %v", err)
	}
	order := install_order(got)
	want_tail := []string{"openvpn/LICENSE", "openvpn/libssl-3-x64.dll", "openvpn/openvpn.exe"}
	if len(order) != len(payload_files)+len(want_tail) || order[0] != service_exe {
		t.Fatalf("install_order = %v, want the fixed files then the openvpn directory", order)
	}
	for i, name := range want_tail {
		if order[len(payload_files)+i] != name {
			t.Fatalf("install_order = %v, want %v at the end", order, want_tail)
		}
	}
}

func Test_a_payload_without_openvpn_is_refused_by_name(t *testing.T) {
	fs := full_payload()
	delete(fs, "openvpn/openvpn.exe")
	_, err := payload_contents(fs)
	if err == nil || !strings.Contains(err.Error(), "openvpn/openvpn.exe") {
		t.Fatalf("payload_contents without openvpn.exe = %v, want a refusal naming it", err)
	}
}

func Test_a_complete_payload_is_read_whole(t *testing.T) {
	got, err := payload_contents(full_payload())
	if err != nil {
		t.Fatalf("payload_contents = %v, want nil", err)
	}
	if len(got) != len(payload_files)+3 {
		t.Fatalf("payload_contents returned %d files, want %d", len(got), len(payload_files)+3)
	}
}

func Test_a_payload_without_wintun_is_refused_by_name(t *testing.T) {
	fs := full_payload()
	delete(fs, wintun_dll)
	_, err := payload_contents(fs)
	if err == nil || !strings.Contains(err.Error(), wintun_dll) {
		t.Fatalf("payload_contents without %s = %v, want a refusal naming it", wintun_dll, err)
	}
}

func Test_an_empty_file_counts_as_missing(t *testing.T) {
	fs := full_payload()
	fs[service_exe] = &fstest.MapFile{}
	_, err := payload_contents(fs)
	if err == nil || !strings.Contains(err.Error(), service_exe) {
		t.Fatalf("payload_contents with an empty %s = %v, want a refusal naming it", service_exe, err)
	}
}

func Test_a_setup_built_without_payload_says_how_to_build_one(t *testing.T) {
	_, err := payload_contents(nil)
	if err == nil || !strings.Contains(err.Error(), "-Setup") {
		t.Fatalf("payload_contents(nil) = %v, want the way to build a real setup", err)
	}
}

func Test_the_uninstall_entry_names_no_version_the_build_did_not_supply(t *testing.T) {
	for _, v := range uninstall_entry(`C:\Program Files\Singularity VPN`, "", 1000) {
		if v.name == "DisplayVersion" {
			t.Fatalf("uninstall_entry with no version carries DisplayVersion = %q, want it absent", v.text)
		}
	}
	found := false
	for _, v := range uninstall_entry(`C:\Program Files\Singularity VPN`, "v1.2.3", 1000) {
		if v.name == "DisplayVersion" && v.text == "v1.2.3" {
			found = true
		}
	}
	if !found {
		t.Fatal("uninstall_entry with a version does not carry it as DisplayVersion")
	}
}

func Test_the_uninstall_command_is_quoted(t *testing.T) {
	dir := `C:\Program Files\Singularity VPN`
	for _, v := range uninstall_entry(dir, "", 0) {
		if v.name == "UninstallString" && v.text != `"C:\Program Files\Singularity VPN\uninstall.exe" uninstall` {
			t.Fatalf("UninstallString = %q, want the path quoted: unquoted, Windows tries C:\\Program first", v.text)
		}
	}
	if got := autostart_command(dir); got != `"C:\Program Files\Singularity VPN\SingularityVPN.exe" --tray` {
		t.Fatalf("autostart_command = %q, want the quoted window path with --tray", got)
	}
}
