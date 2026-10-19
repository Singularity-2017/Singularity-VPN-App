// Package buildinfo carries the few facts that are decided by the build
// rather than by the source: which control plane this binary talks to, and
// which version it is.
package buildinfo

import "fmt"

// Control_plane is the address of the control plane this binary talks to.
//
// The value here is an address from the RFC 5737 documentation range, so a
// binary built from a clean clone connects nowhere. The real address is
// supplied at link time and is never committed -- see the repository boundary
// rule in docs/superpowers/specs/2026-09-15-singularity-vpn-app-design.md.
var Control_plane = "https://198.51.100.1"

// Version, Commit and Built_at are supplied at link time the same way. They
// stay empty in a plain `go build`, and a caller that wants to show them has
// to handle their absence rather than print a placeholder.
var (
	Version  = ""
	Commit   = ""
	Built_at = ""
)

// Update_mirror is the last-resort update channel, a mirror base URL
// compiled into the build from local/app.env (UPDATE_MIRROR). Empty means
// the build has none; the control plane and the mirrors named by signed
// manifests are tried first either way.
var Update_mirror = ""

// Summary describes the build in one line, naming only what the build
// actually told us.
func Summary() string {
	out := "Singularity VPN"
	if Version != "" {
		out += " " + Version
	}
	if Commit != "" {
		out += " (" + Commit + ")"
	}
	if Built_at != "" {
		out += ", built " + Built_at
	}
	return fmt.Sprintf("%s, control plane %s", out, Control_plane)
}
