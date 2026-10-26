package main

import (
	"debug/buildinfo"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Test_artifact_version_uses_the_exact_linker_variable(t *testing.T) {
	const variable = "singularity-vpn-app/internal/buildinfo.Version="
	for _, flags := range []string{
		"-s -w -X " + variable + "v0.1.3",
		"-X '" + variable + "v0.1.3' -H windowsgui",
		"-X \"" + variable + "v0.1.3\"",
		"-X=" + variable + "v0.1.3",
	} {
		got, err := artifact_version_flags(flags)
		if err != nil || got != "v0.1.3" {
			t.Fatalf("version = %q, error = %v", got, err)
		}
	}
	for _, flags := range []string{
		"-X other.Version=v0.1.3",
		"-X 'other.Value= -X " + variable + "v0.1.3'",
		"-X '" + variable + "v0.1.3",
		"-s -w",
	} {
		if _, err := artifact_version_flags(flags); err == nil {
			t.Fatal("invalid or absent version was accepted")
		}
	}
}

// Opt-in integration check for a freshly built setup and its payload programs.
// Only the version is retained; complete linker flags may contain private URLs.
func Test_built_program_versions_agree(t *testing.T) {
	dir, expected := os.Getenv("SVPN_TEST_BUILD_DIR"), os.Getenv("SVPN_TEST_BUILD_VERSION")
	if dir == "" || expected == "" {
		t.Skip("set SVPN_TEST_BUILD_DIR and SVPN_TEST_BUILD_VERSION for built artifact verification")
	}
	for _, name := range []string{"SingularityVPN-setup.exe", "SingularityVPN.exe", "svpn-service.exe", "svpn-ctl.exe", "svpn-swap.exe"} {
		t.Run(name, func(t *testing.T) {
			info, err := buildinfo.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, setting := range info.Settings {
				if setting.Key != "-ldflags" {
					continue
				}
				found = true
				actual, err := artifact_version_flags(setting.Value)
				if err != nil || actual != expected {
					t.Fatalf("embedded version differs from the expected build version: %v", err)
				}
			}
			if !found {
				t.Fatal("no embedded linker settings")
			}
		})
	}
	if strings.Contains(expected, "-dirty") {
		if err := check_setup_version(filepath.Join(dir, "SingularityVPN-setup.exe"), strings.TrimSuffix(expected, "-dirty")); err == nil {
			t.Fatal("dirty setup could be signed as a clean release")
		}
		output := filepath.Join(t.TempDir(), "release-must-not-be-created")
		err := sign([]string{"--key", filepath.Join(t.TempDir(), "must-not-read.key"), "--setup", filepath.Join(dir, "SingularityVPN-setup.exe"), "--version", strings.TrimSuffix(expected, "-dirty"), "--out", output})
		if err == nil || !strings.Contains(err.Error(), "not a release build") {
			t.Fatal("sign did not reject the dirty setup before key access")
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("sign created release output before refusing the dirty setup")
		}
	}
}

func Test_release_version_must_match_clean_embedded_version(t *testing.T) {
	for _, actual := range []string{"v0.1.0", "v0.1.3-dirty", "v0.1.3-dev.123abcd", ""} {
		if err := match_artifact_version(actual, "v0.1.3"); err == nil {
			t.Errorf("embedded version %q was accepted for v0.1.3", actual)
		}
	}
	if err := match_artifact_version("v0.1.3", "v0.1.3"); err != nil {
		t.Fatal(err)
	}
	err := match_artifact_version("SECRET", "v0.1.3")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("refusal leaked unvalidated embedded metadata")
	}
}
