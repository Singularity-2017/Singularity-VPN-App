package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// The files an installation consists of, in the order they are written.
// The service goes first: everything else is useless without it, and a
// failure on it leaves nothing half-replaced behind.
const (
	service_exe = "svpn-service.exe"
	window_exe  = "SingularityVPN.exe"
	ctl_exe     = "svpn-ctl.exe"
	swap_exe    = "svpn-swap.exe"
	wintun_dll  = "wintun.dll"
	// The packet driver for games-only mode, shipped unmodified under the
	// LGPLv3 with its licence beside it.
	windivert_dll     = "WinDivert.dll"
	windivert_sys     = "WinDivert64.sys"
	windivert_licence = "WinDivert-LICENSE.txt"
	uninstaller       = "uninstall.exe"
	// openvpn_dir holds openvpn.exe with the DLLs of its release, shipped
	// unmodified under the GPLv2 with its licence beside it. Which DLLs a
	// release has is the release's business: the whole directory is the
	// payload, and only the program and the licence are required by name.
	openvpn_dir = "openvpn"
)

var openvpn_required = []string{openvpn_dir + "/openvpn.exe", openvpn_dir + "/LICENSE"}

var payload_files = []string{service_exe, window_exe, ctl_exe, swap_exe, wintun_dll, windivert_dll, windivert_sys, windivert_licence}

// payload_contents reads every payload file from source, refusing a payload
// that lacks any of them by name: an installation without wintun.dll
// installs fine and then cannot bring a tunnel up, which is a worse way to
// find out.
func payload_contents(source fs.FS) (map[string][]byte, error) {
	if source == nil {
		return nil, fmt.Errorf("this setup was built without its payload; build it with scripts/build.ps1 -Setup")
	}
	contents := make(map[string][]byte, len(payload_files))
	var missing []string
	for _, name := range payload_files {
		data, err := fs.ReadFile(source, name)
		if err != nil || len(data) == 0 {
			missing = append(missing, name)
			continue
		}
		contents[name] = data
	}
	// Every file of the directory is read, and one that cannot be is
	// missing like a fixed one: openvpn.exe without one of its DLLs
	// installs fine and then never starts.
	entries, err := fs.ReadDir(source, openvpn_dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading the payload's %s: %w", openvpn_dir, err)
	}
	for _, entry := range entries {
		name := path.Join(openvpn_dir, entry.Name())
		if entry.IsDir() {
			return nil, fmt.Errorf("the payload's %s holds a directory, %s; it is installed flat", openvpn_dir, name)
		}
		data, err := fs.ReadFile(source, name)
		if err != nil || len(data) == 0 {
			missing = append(missing, name)
			continue
		}
		contents[name] = data
	}
	for _, name := range openvpn_required {
		if _, ok := contents[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the payload is missing %s", strings.Join(missing, ", "))
	}
	return contents, nil
}

// install_order is every file of the payload in the order it is written:
// the fixed ones first, then the OpenVPN directory by name.
func install_order(contents map[string][]byte) []string {
	names := append([]string{}, payload_files...)
	var extra []string
	for name := range contents {
		if strings.HasPrefix(name, openvpn_dir+"/") {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(names, extra...)
}

// reg_value is one value of the Add/Remove Programs entry.
type reg_value struct {
	name   string
	text   string
	number uint32
	is_dw  bool
}

// uninstall_entry lists the values Windows shows under Installed apps.
// DisplayVersion is left out when the build supplied no version: the
// entry then shows none, which is true, rather than one nobody built.
func uninstall_entry(dir, version string, size_kb uint32) []reg_value {
	values := []reg_value{
		{name: "DisplayName", text: "Singularity VPN"},
		{name: "Publisher", text: "Singularity"},
		{name: "InstallLocation", text: dir},
		{name: "DisplayIcon", text: dir + `\` + window_exe},
		{name: "UninstallString", text: `"` + dir + `\` + uninstaller + `" uninstall`},
		{name: "NoModify", number: 1, is_dw: true},
		{name: "NoRepair", number: 1, is_dw: true},
		{name: "EstimatedSize", number: size_kb, is_dw: true},
	}
	if version != "" {
		values = append(values, reg_value{name: "DisplayVersion", text: version})
	}
	return values
}

// autostart_command is what the Run key starts at logon: the window,
// straight into the tray, since a person logging in did not ask to see it.
func autostart_command(dir string) string {
	return `"` + dir + `\` + window_exe + `" --tray`
}
