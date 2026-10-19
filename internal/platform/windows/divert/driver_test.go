package divert

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fake_scm struct {
	installed  bool
	delete_err error
	deletes    int
}

func (f *fake_scm) exists() (bool, error) { return f.installed, nil }

func (f *fake_scm) stop_and_delete() error {
	f.deletes++
	if f.delete_err != nil {
		return f.delete_err
	}
	f.installed = false
	return nil
}

func new_driver(t *testing.T, scm *fake_scm) *Driver {
	return &Driver{scm: scm, marker: filepath.Join(t.TempDir(), "windivert.ours")}
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
	scm := &fake_scm{installed: true}
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
	scm.installed = true // GoodbyeDPI, later
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
	scm.installed = true
	if removal, _ := d.Remove(); removal != Removal_foreign {
		t.Fatalf("the next program's driver was claimed: %v", removal)
	}
}
