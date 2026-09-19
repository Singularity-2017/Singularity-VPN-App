// Package connection is the state machine: what it means to be connecting,
// connected, degraded, switching, disconnecting or off, and every allowed
// move between them. It orchestrates -- deciding when to provision a
// profile, when to bring a protocol up, what a tick's probe means, and when
// recovery should try another target -- but it contains no platform code,
// no HTTP and no process control of its own: everything impure is an
// interface defined in internal/api, internal/protocols,
// internal/diagnostics or internal/recovery, and this package only ever
// drives them.
package connection
