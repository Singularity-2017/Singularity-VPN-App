package divert

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fake_scm struct {
	installed  bool
	path       string // the driver file; "" is ours
	delete_err error
	deletes    int
}

const (
	our_dir     = `C:\Program Files\Singularity VPN`
	foreign_sys = `C:\Tools\GoodbyeDPI\WinDivert64.sys`
)

func (f *fake_scm) image() (string, bool, error) {
	if !f.installed {
		return "", false, nil
	}
	if f.path == "" {
		return `\??\` + our_dir + `\WinDivert64.sys`, true, nil
	}
	return f.path, true, nil
}

func (f *fake_scm) stop_and_delete() error {
	f.deletes++
	if f.delete_err != nil {
		return f.delete_err
	}
	f.installed = false
	return nil
}

func new_driver(t *testing.T, scm *fake_scm) *Driver {
	return &Driver{scm: scm, marker: filepath.Join(t.TempDir(), "windivert.ours"), dir: our_dir}
}

func Test_a_driver_we_install_is_ours_to_remove(t *testing.T) {
	scm := &fake_scm{}
	d := new_driver(t, scm)
	ours, err := d.Before_open()
	if err != nil || !ours {
		t.Fatalf("Before_open = %v, %v; want ours", ours, err)
	}
	scm.installed = true // the open installed it

	removal, err := d.Remove()
	if err != nil || removal != Removal_removed || scm.deletes != 1 {
		t.Fatalf("Remove = %v, %v, %d deletes; want removed once", removal, err, scm.deletes)
	}
	if _, err := os.Stat(d.marker); !os.IsNotExist(err) {
		t.Fatalf("marker still there after removal: %v", err)
	}
}

func Test_a_driver_someone_else_installed_is_left_alone(t *testing.T) {
	scm := &fake_scm{installed: true, path: foreign_sys}
	d := new_driver(t, scm)
	ours, err := d.Before_open()
	if err != nil || ours {
		t.Fatalf("Before_open = %v, %v; want not ours", ours, err)
	}
	if exists, ours, err := d.State(); err != nil || !exists || ours {
		t.Fatalf("State = %v, %v, %v; want installed, not ours", exists, ours, err)
	}
	removal, err := d.Remove()
	if err != nil || removal != Removal_foreign || scm.deletes != 0 {
		t.Fatalf("Remove = %v, %v, %d deletes; want foreign, untouched", removal, err, scm.deletes)
	}
}

func Test_a_failed_removal_keeps_the_driver_ours(t *testing.T) {
	scm := &fake_scm{}
	d := new_driver(t, scm)
	if _, err := d.Before_open(); err != nil {
		t.Fatal(err)
	}
	scm.installed = true
	scm.delete_err = errors.New("still loaded")
	if _, err := d.Remove(); err == nil {
		t.Fatal("Remove hid the failure")
	}

	scm.delete_err = nil
	removal, err := d.Remove()
	if err != nil || removal != Removal_removed {
		t.Fatalf("second Remove = %v, %v; want removed", removal, err)
	}
}

func Test_a_failed_install_does_not_claim_the_next_driver(t *testing.T) {
	scm := &fake_scm{}
	d := new_driver(t, scm)
	if _, err := d.Open(func() error { return errors.New("driver blocked") }); err == nil {
		t.Fatal("Open hid the failure")
	}
	scm.installed, scm.path = true, foreign_sys // GoodbyeDPI, later
	if removal, err := d.Remove(); err != nil || removal != Removal_foreign || scm.deletes != 0 {
		t.Fatalf("Remove = %v, %v, %d deletes; want the other program's driver left alone", removal, err, scm.deletes)
	}
}

func Test_a_successful_open_keeps_the_driver_ours(t *testing.T) {
	scm := &fake_scm{}
	d := new_driver(t, scm)
	ours, err := d.Open(func() error { scm.installed = true; return nil })
	if err != nil || !ours {
		t.Fatalf("Open = %v, %v; want ours", ours, err)
	}
	if _, ours, _ := d.State(); !ours {
		t.Fatal("the driver we installed is not recorded as ours")
	}
}

func Test_a_marker_without_a_driver_is_forgotten(t *testing.T) {
	scm := &fake_scm{}
	d := new_driver(t, scm)
	if _, err := d.Before_open(); err != nil {
		t.Fatal(err)
	}
	// Removed by hand; then another program installs its own.
	removal, err := d.Remove()
	if err != nil || removal != Removal_absent {
		t.Fatalf("Remove = %v, %v; want absent", removal, err)
	}
	scm.installed, scm.path = true, foreign_sys
	if removal, _ := d.Remove(); removal != Removal_foreign {
		t.Fatalf("the next program's driver was claimed: %v", removal)
	}
}

// Our driver deleted by hand, the marker left behind, then another
// program's driver installed: the marker must not claim it.
func Test_a_stale_marker_does_not_claim_another_programs_driver(t *testing.T) {
	scm := &fake_scm{}
	d := new_driver(t, scm)
	if _, err := d.Open(func() error { scm.installed = true; return nil }); err != nil {
		t.Fatal(err)
	}
	scm.installed, scm.path = true, foreign_sys // sc delete, then GoodbyeDPI
	if _, ours, err := d.State(); err != nil || ours {
		t.Fatalf("State = ours %v, %v; want the other program's", ours, err)
	}
	if removal, err := d.Remove(); err != nil || removal != Removal_foreign || scm.deletes != 0 {
		t.Fatalf("Remove = %v, %v, %d deletes; want it left alone", removal, err, scm.deletes)
	}
	scm.path = "" // even if a driver from our directory appears later
	if _, ours, _ := d.State(); ours {
		t.Fatal("the stale marker survived")
	}
}

func Test_the_driver_path_is_compared_by_directory(t *testing.T) {
	for path, want := range map[string]bool{
		`\??\C:\Program Files\Singularity VPN\WinDivert64.sys`: true,
		`"c:\program files\singularity vpn\WinDivert64.sys"`:   true,
		`C:\Program Files\Singularity VPN\sub\WinDivert64.sys`: false,
		foreign_sys: false,
		"":          false,
	} {
		if got := same_dir(path, our_dir); got != want {
			t.Errorf("same_dir(%q) = %v; want %v", path, got, want)
		}
	}
}
