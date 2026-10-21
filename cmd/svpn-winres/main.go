// Command svpn-winres writes the application icon, as a resource object,
// beside each main package that should carry it. The build runs it first;
// the objects are generated, never committed.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"singularity-vpn-app/internal/appicon"
)

// object is the name the Go toolchain links into windows/amd64 builds only.
const object = "rsrc_windows_amd64.syso"

func main() {
	dirs := os.Args[1:]
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: svpn-winres <main package dir>...")
		os.Exit(2)
	}
	data := appicon.Syso()
	for _, dir := range dirs {
		if err := os.WriteFile(filepath.Join(dir, object), data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
