package divert

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Service_name is the service WinDivert installs its driver as. Every
// program that ships WinDivert uses the same name, which is why ownership
// has to be remembered rather than assumed.
const Service_name = "WinDivert"

// service_control is the part of the service control manager Driver uses.
type service_control interface {
	// exists says whether the WinDivert service is installed.
	exists() (bool, error)
	// stop_and_delete stops the driver and deletes its service, and
	// fails if the driver is still loaded afterwards.
	stop_and_delete() error
}

// Driver decides whether the WinDivert driver on this machine is ours to
// remove. It is ours only when our own open is what installed it: a
// WinDivert service already present belongs to another program
// (GoodbyeDPI, zapret) that would break if it were deleted.
type Driver struct {
	scm service_control
	// marker is a file whose presence records that we installed the
	// driver. It lives in the service's protected data directory.
	marker string
}

// Removal is what Remove did.
type Removal int

const (
	// Removal_absent: there was no driver to remove.
	Removal_absent Removal = iota
	// Removal_removed: ours, stopped and deleted.
	Removal_removed
	// Removal_foreign: present, but another program installed it; left
	// in place. A game with a touchy anti-cheat may still see it, and the
	// person has to be told which program it belongs to.
	Removal_foreign
)

// Before_open must run before the first Library.Open; Open wraps both and
// is what callers use. When the service does
// not exist yet the open is about to install it, so the marker is written
// first -- written before, not after, so that a crash between the two
// leaves a driver we will still remove rather than one we disown. It
// reports whether the driver is (or is about to be) ours.
func (d *Driver) Before_open() (bool, error) {
	exists, err := d.scm.exists()
	if err != nil {
		return false, err
	}
	if exists {
		return d.marked()
	}
	if err := os.WriteFile(d.marker, []byte(Service_name+" was installed by this program\n"), 0o600); err != nil {
		return false, fmt.Errorf("divert: recording the driver as ours: %w", err)
	}
	return true, nil
}

// Open runs open -- the first Library.Open -- with the driver's ownership
// recorded around it: Before_open first, and if open fails while the
// service is still absent, the marker is taken back. Without that, a failed
// install (no WinDivert64.sys, Memory integrity) would leave a marker that
// claims the next program's driver as ours.
func (d *Driver) Open(open func() error) (ours bool, err error) {
	ours, err = d.Before_open()
	if err != nil {
		return false, err
	}
	if open_err := open(); open_err != nil {
		if exists, err := d.scm.exists(); err == nil && !exists {
			if err := d.unmark(); err != nil {
				return false, errors.Join(open_err, err)
			}
		}
		return false, open_err
	}
	return ours, nil
}

// Remove takes the driver off the machine if it is ours. Every handle must
// be closed first; otherwise the driver cannot unload and Remove fails,
// keeping the marker so the next attempt still knows the driver is ours.
func (d *Driver) Remove() (Removal, error) {
	exists, err := d.scm.exists()
	if err != nil {
		return Removal_absent, err
	}
	ours, err := d.marked()
	if err != nil {
		return Removal_absent, err
	}
	switch {
	case !exists:
		// Gone already, by our earlier attempt or by hand; a marker left
		// behind would claim the next program's driver as ours.
		return Removal_absent, d.unmark()
	case !ours:
		return Removal_foreign, nil
	}
	if err := d.scm.stop_and_delete(); err != nil {
		return Removal_absent, err
	}
	return Removal_removed, d.unmark()
}

// State reports whether the driver is installed and, if so, whether it is
// ours. It changes nothing.
func (d *Driver) State() (exists, ours bool, err error) {
	exists, err = d.scm.exists()
	if err != nil || !exists {
		return false, false, err
	}
	ours, err = d.marked()
	return exists, ours, err
}

func (d *Driver) marked() (bool, error) {
	_, err := os.Stat(d.marker)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("divert: reading whether the driver is ours: %w", err)
}

func (d *Driver) unmark() error {
	if err := os.Remove(d.marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("divert: forgetting the driver: %w", err)
	}
	return nil
}
