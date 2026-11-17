package platform

import (
	"net"
	"testing"
)

func TestDetectVpnConflictFrom(t *testing.T) {
	ifaces := []net.Interface{
		{Name: "Ethernet", Flags: net.FlagUp},
		{Name: "Singularity Tunnel", Flags: net.FlagUp},
		{Name: "happ-tun", Flags: net.FlagUp},
	}
	got := Detect_vpn_conflict_from(ifaces)
	if got == nil || got.Name != "Happ" || got.Adapter != "happ-tun" {
		t.Fatalf("expected Happ conflict, got %+v", got)
	}

	down := []net.Interface{
		{Name: "happ-tun", Flags: 0},
		{Name: "Singularity Tunnel", Flags: net.FlagUp},
	}
	if got := Detect_vpn_conflict_from(down); got != nil {
		t.Fatalf("expected nil when interface is down, got %+v", got)
	}
}
