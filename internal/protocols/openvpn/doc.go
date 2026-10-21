// Package openvpn drives the bundled openvpn.exe (2.6, the last line that
// runs on wintun) on a wintun adapter the service creates for it. The
// profile check, the log reading and the connect/disconnect lifecycle are
// pure and tested on fakes; starting the process, its job object and the
// adapter (windows.go) are verified by hand on a real machine.
package openvpn
