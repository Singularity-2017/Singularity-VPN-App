// Package ipc is the protocol spoken between svpn-service.exe, which runs as
// LocalSystem and owns the tunnel, and SingularityVPN.exe, which runs as the
// logged-in user and owns only the window. They speak newline-delimited
// JSON over the named pipe \\.\pipe\SingularityVPN -- docs/WINDOWS_CLIENT.md
// section 22, docs/superpowers/specs/2026-09-15-singularity-vpn-app-design.md
// section 2.
//
// There is no Windows code in this package and no platform dependency: it
// runs over any net.Conn, so net.Pipe() exercises everything here without an
// operating system in between. The pipe itself -- dialling it and listening
// on it -- is specific to Windows and belongs to a later stage; this package
// only defines what travels once a connection exists.
//
// docs/WINDOWS_CLIENT.md section 45 shapes everything here: a Windows
// service must not become an arbitrary privileged command executor, so the
// command set this package carries is closed -- a fixed list of constants,
// dispatched through a switch an unrecognised name falls out of by name, not
// a map a caller on the other end of the pipe could ever extend.
package ipc
