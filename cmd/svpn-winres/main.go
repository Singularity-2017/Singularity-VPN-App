// Command svpn-winres writes, as a resource object beside each main package,
// what Explorer shows of the program: the application icon on the window and
// the setup, and on every program the version from -version with its name
// and description. The build runs it first; the objects are generated, never
// committed. Without -version a program that carries no icon gets no object,
// so a build that was not told its version does not claim one.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"singularity-vpn-app/internal/appicon"
)

// object is the name the Go toolchain links into windows/amd64 builds only.
const object = "rsrc_windows_amd64.syso"

type program struct {
	file, description string
	icon              bool
}

// programs are the main packages the build names, by directory.
var programs = map[string]program{
	"singularityvpn": {"SingularityVPN.exe", appicon.Product, true},
	"svpn-setup":     {"SingularityVPN-setup.exe", appicon.Product + " setup", true},
	"svpn-service":   {"svpn-service.exe", appicon.Product + " service", false},
	"svpn-ctl":       {"svpn-ctl.exe", appicon.Product + " console control", false},
	"svpn-swap":      {"svpn-swap.exe", appicon.Product + " updater", false},
	"svpn-divert":    {"svpn-divert.exe", appicon.Product + " WinDivert harness", false},
	"svpn-probe":     {"svpn-probe.exe", appicon.Product + " tunnel harness", false},
}

func main() {
	version := flag.String("version", "", "the build's version, such as v0.1.5-dev.84096d5")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: svpn-winres [-version v] <main package dir>...")
		os.Exit(2)
	}
	for _, dir := range flag.Args() {
		if err := write(dir, *version); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func write(dir, version string) error {
	name := filepath.Base(filepath.Clean(dir))
	p, known := programs[name]
	if !known {
		return fmt.Errorf("svpn-winres: %s is not a program this build knows", dir)
	}
	path := filepath.Join(dir, object)
	if !p.icon && version == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	// Only the setup carries a manifest: Windows treats it as an installer
	// by its name. The window keeps none, so nothing about its scaling
	// changes.
	manifest := ""
	if name == "svpn-setup" {
		manifest = appicon.Setup_manifest
	}
	var info *appicon.Version_info
	if version != "" {
		info = &appicon.Version_info{Version: version, Description: p.description, File: p.file}
	}
	data, err := appicon.Syso(p.icon, manifest, info)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
