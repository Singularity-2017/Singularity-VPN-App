// Package routing computes the operating-system state a tunnel needs --
// addresses, routes and resolvers -- without touching the system and without
// knowing which protocol produced the tunnel. A protocol adapter (see
// internal/protocols/wireguard) describes what its tunnel needs as a
// Tunnel; Plan_for turns that into a Plan that an elevated, hand-verified
// layer applies with the Windows routing APIs.
//
// This package is pure: no network, no operating system, no administrator.
// It must never import a protocol package -- the dependency runs the other
// way: a protocol package may import this one, not the reverse.
package routing
