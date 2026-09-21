// Package platform hides whether an operation runs in this process or
// through the service. The core never learns which.
//
// Network is the neutral interface a protocol adapter drives to bring a
// tunnel's routing.Plan onto the machine and take it back off. It mentions
// no operating system and no protocol; internal/platform/windows is the
// Windows implementation, over winipcfg.
package platform
