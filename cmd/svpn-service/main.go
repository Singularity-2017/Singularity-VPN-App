// Command svpn-service is the Windows service that owns the tunnel, the
// routes, the DNS and every call to the control plane.
//
// It does none of that yet. Today it prints what the build put into it, which
// is the one thing worth having at this stage: proof that the link-time
// values in internal/buildinfo actually arrive.
package main

import (
	"fmt"

	"singularity-vpn-app/internal/buildinfo"
)

func main() {
	fmt.Println(buildinfo.Summary())
}
