//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// These run against the real file system in a temporary directory. Assigning
// Administrators as owner takes an elevated token, so unelevated they step
// aside rather than fail.

func require_elevated(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated token to assign Administrators as owner")
	}
}

func security_of(t *testing.T, path string) string {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("reading the security of %s: %v", path, err)
	}
	return descriptor.String()
}

func Test_a_new_data_directory_admits_only_the_service_and_administrators(t *testing.T) {
	require_elevated(t)
	dir := filepath.Join(t.TempDir(), "data")
	if err := ensure_private_directory(dir); err != nil {
		t.Fatalf("ensure_private_directory: %v", err)
	}

	got := security_of(t, dir)
	for _, want := range []string{"O:BA", "D:P", "(A;OICI;FA;;;SY)", "(A;OICI;FA;;;BA)", "(A;OICI;RC;;;OW)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the directory's security is %s, missing %s", got, want)
		}
	}
	if strings.Contains(got, ";BU)") || strings.Contains(got, ";WD)") || strings.Contains(got, ";AU)") {
		t.Fatalf("the directory's security is %s: it admits ordinary users", got)
	}
}

// Test_a_file_inside_inherits_the_restriction is the one that matters: the
// directory is only the container, and secrets.json is what has to be shut.
func Test_a_file_inside_inherits_the_restriction(t *testing.T) {
	require_elevated(t)
	dir := filepath.Join(t.TempDir(), "data")
	if err := ensure_private_directory(dir); err != nil {
		t.Fatalf("ensure_private_directory: %v", err)
	}
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := security_of(t, file)
	for _, want := range []string{"SY)", "BA)", "(A;ID;RC;;;OW)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the file's security is %s, missing %s", got, want)
		}
	}
	if strings.Contains(got, ";BU)") || strings.Contains(got, ";WD)") || strings.Contains(got, ";AU)") {
		t.Fatalf("the file's security is %s: it admits ordinary users", got)
	}
}

// Test_an_existing_open_directory_is_repaired guards the case
// CreateDirectory ignores: a directory already there keeps whatever it had,
// so the repair path is the only thing that closes it.
func Test_an_existing_open_directory_is_repaired(t *testing.T) {
	require_elevated(t)
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	open_descriptor, err := windows.SecurityDescriptorFromString(`D:P(A;OICI;GA;;;WD)`)
	if err != nil {
		t.Fatal(err)
	}
	open_dacl, _, _ := open_descriptor.DACL()
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, open_dacl, nil); err != nil {
		t.Fatalf("opening the directory up for the test: %v", err)
	}

	if err := ensure_private_directory(dir); err != nil {
		t.Fatalf("ensure_private_directory over an existing directory: %v", err)
	}
	got := security_of(t, dir)
	if strings.Contains(got, ";WD)") {
		t.Fatalf("the directory's security is still %s: an existing open directory was left open", got)
	}
	if !strings.Contains(got, "O:BA") {
		t.Fatalf("the directory's security is %s, want Administrators as owner", got)
	}
}
