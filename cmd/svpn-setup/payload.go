package main

import (
	"fmt"
	"io/fs"
	"strings"
)

// The files an installation consists of, in the order they are written.
// The service goes first: everything else is useless without it, and a
// failure on it leaves nothing half-replaced behind.
const (
	service_exe = "svpn-service.exe"
	window_exe  = "SingularityVPN.exe"
	ctl_exe     = "svpn-ctl.exe"
	wintun_dll  = "wintun.dll"
	uninstaller = "uninstall.exe"
)

var payload_files = []string{service_exe, window_exe, ctl_exe, wintun_dll}

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
	if len(missing) > 0 {
		return nil, fmt.Errorf("the payload is missing %s", strings.Join(missing, ", "))
	}
	return contents, nil
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
