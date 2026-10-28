package appicon

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func Test_a_dev_build_is_its_number_marked_prerelease(t *testing.T) {
	for _, c := range []struct {
		in         string
		want       [3]uint16
		prerelease bool
	}{
		{"v0.1.5-dev.84096d5", [3]uint16{0, 1, 5}, true},
		{"v0.1.4", [3]uint16{0, 1, 4}, false},
		{"1.20.3-dirty", [3]uint16{1, 20, 3}, true},
	} {
		got, prerelease, err := numeric(c.in)
		if err != nil || got != c.want || prerelease != c.prerelease {
			t.Errorf("numeric(%q) = %v, %v, %v; want %v, %v", c.in, got, prerelease, err, c.want, c.prerelease)
		}
	}
	if _, _, err := numeric("dev"); err == nil {
		t.Error("a version without X.Y.Z was accepted")
	}
}

// version_string finds the value Explorer reads for key: the key in UTF-16,
// then, on the next four bytes, the value up to its terminator.
func version_string(block []byte, key string) string {
	k := utf16z(key)
	at := bytes.Index(block, k)
	if at < 0 {
		return ""
	}
	at += len(k)
	for at%4 != 0 {
		at++
	}
	var units []uint16
	for ; at+1 < len(block); at += 2 {
		u := binary.LittleEndian.Uint16(block[at:])
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return string(utf16.Decode(units))
}

func Test_the_version_block_says_the_number_and_the_names(t *testing.T) {
	block, err := version_resource(Version_info{Version: "v0.1.5-dev.84096d5", Description: "Singularity VPN service", File: "svpn-service.exe"})
	if err != nil {
		t.Fatal(err)
	}
	if got := int(binary.LittleEndian.Uint16(block)); got != len(block) {
		t.Errorf("the block says it is %d bytes, it is %d", got, len(block))
	}
	fixed := bytes.Index(block, []byte{0xBD, 0x04, 0xEF, 0xFE})
	if fixed < 0 || fixed%4 != 0 {
		t.Fatalf("the fixed part is not on four bytes: at %d", fixed)
	}
	if ms, ls := binary.LittleEndian.Uint32(block[fixed+8:]), binary.LittleEndian.Uint32(block[fixed+12:]); ms != 0x00000001 || ls != 0x00050000 {
		t.Errorf("file version %08x %08x, want 0.1.5.0", ms, ls)
	}
	if flags := binary.LittleEndian.Uint32(block[fixed+28:]); flags != 0x2 {
		t.Errorf("flags %x, want pre-release", flags)
	}
	for key, want := range map[string]string{
		"FileVersion":      "0.1.5-dev.84096d5",
		"ProductVersion":   "0.1.5-dev.84096d5",
		"ProductName":      Product,
		"FileDescription":  "Singularity VPN service",
		"OriginalFilename": "svpn-service.exe",
	} {
		if got := version_string(block, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
