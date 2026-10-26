//go:build windows

package wireguard

import (
	"testing"

	"golang.org/x/sys/windows"
)

func Test_adapter_guid_is_deterministic_and_distinct_per_name(t *testing.T) {
	first := adapter_guid("Singularity")
	second := adapter_guid("Singularity")
	if first != second {
		t.Fatalf("adapter_guid(Singularity) changed between calls: %v vs %v", first, second)
	}
	if first == (windows.GUID{}) {
		t.Fatal("adapter_guid returned a zero GUID")
	}
	other := adapter_guid("Singularity-Probe")
	if first == other {
		t.Fatalf("distinct adapter names produced the same GUID: %v", first)
	}
	if version := (first.Data3 >> 12) & 0x0f; version != 5 {
		t.Fatalf("GUID version nibble = %d, want 5", version)
	}
	if variant := (first.Data4[0] >> 6) & 0x03; variant != 2 {
		t.Fatalf("GUID variant bits = %d, want 2 (RFC 4122)", variant)
	}
}
