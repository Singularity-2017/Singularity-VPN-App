//go:build windows

// Command svpn-swap applies an update. The service launches it, as
// LocalSystem, from its private update directory, and it outlives the
// service it replaces. It has no network access of its own: it runs the
// verified setup quietly, then waits for the new service to prove itself
// healthy -- the pipe answers, the version is the one expected, a status
// call succeeds -- and if that does not happen within sixty seconds it runs
// the previous version's setup to put things back (design section 6).
//
// The previous version's setup is already on disk: every installation keeps
// a copy of the setup that made it as uninstall.exe, payload and all. That
// copy is the one previous version kept.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/update"
)

const health_deadline = 60 * time.Second

func main() {
	setup := flag.String("setup", "", "the verified setup to apply")
	version := flag.String("version", "", "the version it installs")
	dir := flag.String("dir", "", "the update directory")
	flag.Parse()
	if *setup == "" || *version == "" || *dir == "" {
		fmt.Fprintln(os.Stderr, "svpn-swap is started by the Singularity VPN service; it is not run by hand.")
		os.Exit(2)
	}

	log_file, err := os.OpenFile(filepath.Join(*dir, "swap.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		defer log_file.Close()
		log.SetOutput(log_file)
	} else {
		// Started detached, so there is no console: stderr is not a handle
		// a child process could inherit.
		log.SetOutput(io.Discard)
	}
	log.Printf("applying %s from %s", *version, *setup)

	if err := swap(*setup, *version, *dir); err != nil {
		log.Printf("swap failed: %v", err)
		os.Exit(1)
	}
	log.Printf("%s is installed and healthy", *version)
}

func swap(setup, version, dir string) error {
	program_files, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return err
	}
	install_dir := filepath.Join(program_files, "Singularity VPN")

	// Keep the setup that made the current installation before the new
	// one overwrites it.
	previous := filepath.Join(dir, "previous-setup.exe")
	has_previous := copy_file(filepath.Join(install_dir, "uninstall.exe"), previous) == nil
	if !has_previous {
		log.Printf("no previous setup to fall back to (an installation made by hand); going ahead without one")
	}

	if err := run_setup(setup); err != nil {
		log.Printf("the new setup failed: %v", err)
		return roll_back(previous, has_previous, version, dir)
	}
	if err := wait_healthy(version); err != nil {
		log.Printf("the new service did not prove itself healthy: %v", err)
		return roll_back(previous, has_previous, version, dir)
	}
	return update.Clear_staged(dir)
}

func roll_back(previous string, has_previous bool, version, dir string) error {
	// Recording the failure matters: without it the next check stages the
	// same release again and the swap repeats every few hours. A sharing
	// violation from a scanner holding update.json is the likely cause of
	// a failure here, and it passes.
	var rerr error
	for attempt := 0; attempt < 10; attempt++ {
		if rerr = update.Record_failure(dir, version, time.Now()); rerr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if rerr != nil {
		log.Printf("could not record %s as failed: %v", version, rerr)
	}
	if !has_previous {
		return fmt.Errorf("%s failed and there is no previous version to restore", version)
	}
	log.Printf("restoring the previous version")
	if err := run_setup(previous); err != nil {
		return fmt.Errorf("%s failed, and restoring the previous version failed too: %w", version, err)
	}
	return fmt.Errorf("%s failed; the previous version was restored", version)
}

// setup_timeout bounds one quiet setup run. The setup stops the service
// before it writes anything; a setup that then hung would leave the VPN
// down with the rollback never reached.
const setup_timeout = 10 * time.Minute

func run_setup(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), setup_timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "install", "--quiet")
	cmd.Stdout = log.Writer()
	cmd.Stderr = log.Writer()
	return cmd.Run()
}

// wait_healthy gives the new service health_deadline to answer on the
// pipe with the expected version and a working status call.
func wait_healthy(version string) error {
	deadline := time.Now().Add(health_deadline)
	var last error
	for time.Now().Before(deadline) {
		last = check_once(version)
		if last == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return last
}

func check_once(version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := ipc.Dial(ctx)
	if err != nil {
		return err
	}
	client := ipc.New_client(conn)
	defer client.Close()
	v, err := client.Version(ctx)
	if err != nil {
		return err
	}
	got, got_ok := update.Parse_version(v.Version)
	want, want_ok := update.Parse_version(version)
	if !got_ok || !want_ok || got != want {
		// Compared as versions, not strings: the manifest may say 1.2.3
		// where the build reports v1.2.3.
		return fmt.Errorf("the service reports version %q, want %q", v.Version, version)
	}
	_, err = client.Status(ctx)
	return err
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
