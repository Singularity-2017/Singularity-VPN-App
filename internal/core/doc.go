// Package core is the application: it wires every other package together
// and implements ipc.Service, the interface stage 4A defined for the pipe
// to drive. It is the one package allowed to know every other one -- the
// control plane client, the connection state machine, the secure store, the
// protocol adapters, the diagnostics prober and the event bus all meet here
// and nowhere else. Nothing in this module imports core; cmd/ is the only
// other thing that does.
package core
