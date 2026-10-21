//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func descriptor_of(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("reading the descriptor of %s: %v", path, err)
	}
	return sd.String()
}

// Test_the_install_directory_cannot_be_rewritten_by_its_owner guards the
// hole the review found: files an elevated administrator writes are owned by
// their own account, and an owner may rewrite the DACL -- which, under
// Program Files, would let any program they run unelevated replace the
// LocalSystem service.
func Test_the_install_directory_cannot_be_rewritten_by_its_owner(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("assigning Administrators as owner needs an elevated session")
	}
	dir := t.TempDir()
	if err := secure_install_dir(dir); err != nil {
		t.Fatalf("secure_install_dir: %v", err)
	}
	file := filepath.Join(dir, service_exe)
	if err := write_replacing(file, []byte("binary")); err != nil {
		t.Fatalf("write_replacing: %v", err)
	}

	got := descriptor_of(t, dir)
	for _, want := range []string{"O:BA", "D:P", "(A;OICI;FA;;;SY)", "(A;OICI;FA;;;BA)", "(A;OICI;0x1200a9;;;BU)", "(A;OICI;RC;;;OW)"} {
		if !strings.Contains(got, want) {
			t.Errorf("directory descriptor %s lacks %s", got, want)
		}
	}
	if strings.Contains(got, ";;;WD)") || strings.Contains(got, ";;;AU)") {
		t.Errorf("directory descriptor %s grants Everyone or Authenticated Users something", got)
	}

	got = descriptor_of(t, file)
	if !strings.HasPrefix(got, "O:BA") {
		t.Errorf("file descriptor %s: owner is not Administrators", got)
	}
	// The file must not grant Users more than read and execute, nor anything
	// to Everyone or Authenticated Users; its owner -- the one principal
	// with an implicit WRITE_DAC -- was checked above.
	if strings.Contains(got, ";;;WD)") || strings.Contains(got, ";;;AU)") || strings.Contains(got, "FA;;;BU)") {
		t.Errorf("file descriptor %s grants ordinary users too much", got)
	}
	if _, err := os.Stat(file + ".new"); !os.IsNotExist(err) {
		t.Errorf("the temporary %s.new was left behind", service_exe)
	}
}

func Test_a_setup_from_the_gamers_page_is_told_by_its_name(t *testing.T) {
	for name, want := range map[string]bool{
		"SingularityVPN-gamer-setup.exe":     true,
		"SingularityVPN-Gamer-setup (1).exe": true,
		"SingularityVPN-setup-0.1.0.exe":     false,
	} {
		if got := is_gamer_setup(name); got != want {
			t.Errorf("is_gamer_setup(%q) = %v, want %v", name, got, want)
		}
	}
}
