//go:build windows

package divert

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// New_driver returns the Driver for this machine. marker is the file that
// records the driver as ours; it belongs in a directory only SYSTEM and
// administrators can write, or anyone could make us delete another
// program's driver.
func New_driver(marker string) *Driver {
	return &Driver{scm: system_scm{}, marker: marker}
}

type system_scm struct{}

// stop_wait bounds how long stop_and_delete waits for the driver to unload.
// A driver with no open handles unloads at once; one still held never does.
const stop_wait = 5 * time.Second

func (system_scm) exists() (bool, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return false, fmt.Errorf("divert: connecting to the service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(Service_name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("divert: opening the %s service: %w", Service_name, err)
	}
	service.Close()
	return true, nil
}

func (system_scm) stop_and_delete() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("divert: connecting to the service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(Service_name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("divert: opening the %s service: %w", Service_name, err)
	}
	defer service.Close()

	// Stop first and delete only a stopped driver. A driver still held open
	// -- by us, or by another program that opened the driver we installed
	// -- is left installed and unmarked for deletion, so that program keeps
	// working and the next Remove tries again.
	if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return fmt.Errorf("divert: stopping the %s driver: %w", Service_name, err)
	}
	deadline := time.Now().Add(stop_wait)
	for {
		status, err := service.Query()
		if err != nil {
			return fmt.Errorf("divert: reading the %s driver's state: %w", Service_name, err)
		}
		if status.State == svc.Stopped {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("divert: the %s driver is still loaded after %s; a program still holds it open", Service_name, stop_wait)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("divert: deleting the %s service: %w", Service_name, err)
	}
	return nil
}
