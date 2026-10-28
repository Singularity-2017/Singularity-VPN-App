//go:build windows

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"singularity-vpn-app/internal/buildinfo"
	"singularity-vpn-app/internal/control_address"
	"singularity-vpn-app/internal/core"
	"singularity-vpn-app/internal/update"
)

// create_breakaway_from_job lets svpn-swap outlive the service: if the
// service manager ever puts services in a job that kills its members on
// exit, the swap must not die with the service it is replacing.
const create_breakaway_from_job = 0x01000000

// build_updater wires internal/update to this service. A failure here
// costs updates, never the service: it is logged and nil is returned.
func build_updater(p paths, application *core.Application, addresses *control_address.Store, logger *slog.Logger) *update.Updater {
	logger = logger.With("component", "update")
	// The update directory sits inside the private data directory, so
	// what is staged there and the copy of svpn-swap run from there are
	// writable only by LocalSystem and Administrators.
	dir := filepath.Join(p.root, "update")
	if err := ensure_private_directory(dir); err != nil {
		logger.Warn("updates are off: the update directory could not be prepared", "err", err)
		return nil
	}
	keys, err := update.Trusted_keys()
	if err != nil {
		logger.Warn("updates are off", "err", err)
		return nil
	}
	updater, err := update.New(update.Config{
		Control_planes: addresses.Current,
		Offer_control_planes: func(list []string, released_at time.Time) {
			if _, err := addresses.Offer(list, released_at); err != nil {
				logger.Warn("the signed manifest's control plane addresses were not taken", "err", err)
			}
		},
		Fallback:        buildinfo.Update_mirror,
		Current_version: buildinfo.Version,
		Keys:            keys,
		Dir:             dir,
		Idle: func() bool {
			status, err := application.Status(context.Background())
			return err == nil && (status.State == "disconnected" || status.State == "failed")
		},
		Apply:  func(s update.Staged) error { return launch_swap(dir, s) },
		Logger: logger,
	})
	if err != nil {
		logger.Warn("updates are off", "err", err)
		return nil
	}
	return updater
}

// launch_swap copies svpn-swap beside the staged setup and starts it
// detached. It runs from the update directory, not the install directory,
// because the setup it runs replaces everything in the install directory.
func launch_swap(dir string, staged update.Staged) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	swap := filepath.Join(dir, "svpn-swap.exe")
	if err := copy_file(filepath.Join(filepath.Dir(self), "svpn-swap.exe"), swap); err != nil {
		return err
	}
	args := []string{"--setup", staged.Path, "--version", staged.Version, "--dir", dir}
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS)
	cmd := exec.Command(swap, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags | create_breakaway_from_job}
	if err := cmd.Start(); err != nil {
		// Breakaway is refused when the job does not allow it, and then
		// there is no job to break away from that would matter.
		cmd = exec.Command(swap, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if err := cmd.Start(); err != nil {
			return err
		}
	}
	return cmd.Process.Release()
}

func copy_file(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
