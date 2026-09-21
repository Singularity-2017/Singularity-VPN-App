// Package windows is the Windows implementation of platform: adapters,
// routes, DNS and the service itself.
//
// New_network implements platform.Network over winipcfg. Every file here
// except this one carries a windows build tag, because winipcfg itself only
// compiles for Windows -- this file stays untagged so the package is never
// empty on another platform.
package windows
