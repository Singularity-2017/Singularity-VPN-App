package appicon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"unicode/utf16"
)

// Product and Company are what Explorer's Details tab names every program
// by.
const (
	Product = "Singularity VPN"
	Company = "Singularity"
)

// Version_info is what one program says about itself under Properties ->
// Details.
type Version_info struct {
	// Version is the build's version as the build resolved it, such as
	// v0.1.5-dev.84096d5. Its X.Y.Z becomes the numeric file version.
	Version string
	// Description is the program's name in Task Manager and Details.
	Description string
	// File is the name the program is built under.
	File string
}

var semver = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(.*)$`)

// numeric is the X.Y.Z of a version, and whether anything follows it: a
// pre-release, a dev build or a dirty tree.
func numeric(version string) (parts [3]uint16, prerelease bool, err error) {
	m := semver.FindStringSubmatch(version)
	if m == nil {
		return parts, false, fmt.Errorf("version %q is not X.Y.Z", version)
	}
	for i := range parts {
		n, err := strconv.ParseUint(m[i+1], 10, 16)
		if err != nil {
			return parts, false, fmt.Errorf("version %q: %w", version, err)
		}
		parts[i] = uint16(n)
	}
	return parts, m[4] != "", nil
}

// version_resource is the VS_VERSIONINFO block of an RT_VERSION resource:
// a fixed part with the numbers, then the strings Explorer shows, in US
// English and UTF-16.
func version_resource(info Version_info) ([]byte, error) {
	parts, prerelease, err := numeric(info.Version)
	if err != nil {
		return nil, err
	}
	ms := uint32(parts[0])<<16 | uint32(parts[1])
	ls := uint32(parts[2]) << 16
	var flags uint32
	if prerelease {
		flags = 0x2 // VS_FF_PRERELEASE
	}
	var fixed bytes.Buffer
	for _, v := range []uint32{
		0xFEEF04BD, 0x00010000, // signature, structure version
		ms, ls, ms, ls, // file version, product version
		0x3F, flags, // flags mask, flags
		0x40004,    // VOS_NT_WINDOWS32
		1, 0, 0, 0, // VFT_APP, no subtype, no date
	} {
		_ = binary.Write(&fixed, binary.LittleEndian, v)
	}

	display := info.Version
	if len(display) > 0 && display[0] == 'v' {
		display = display[1:]
	}
	var strings [][]byte
	for _, pair := range [][2]string{
		{"CompanyName", Company},
		{"FileDescription", info.Description},
		{"FileVersion", display},
		{"InternalName", info.File},
		{"OriginalFilename", info.File},
		{"ProductName", Product},
		{"ProductVersion", display},
	} {
		if pair[1] == "" {
			continue
		}
		text := utf16z(pair[1])
		strings = append(strings, version_node(pair[0], text, uint16(len(text)/2), true))
	}
	table := version_node("040904B0", nil, 0, true, strings...) // en-US, Unicode
	string_info := version_node("StringFileInfo", nil, 0, true, table)
	translation := []byte{0x09, 0x04, 0xB0, 0x04}
	var_info := version_node("VarFileInfo", nil, 0, true, version_node("Translation", translation, 4, false))
	return version_node("VS_VERSION_INFO", fixed.Bytes(), uint16(fixed.Len()), false, string_info, var_info), nil
}

// version_node is one block of the version resource: its length, the
// length of its value (in characters for text, bytes otherwise), its type,
// its key, then its value and children, each starting on four bytes.
func version_node(key string, value []byte, value_length uint16, text bool, children ...[]byte) []byte {
	var out bytes.Buffer
	out.Write(make([]byte, 6))
	out.Write(utf16z(key))
	pad := func() {
		for out.Len()%4 != 0 {
			out.WriteByte(0)
		}
	}
	pad()
	out.Write(value)
	for _, child := range children {
		pad()
		out.Write(child)
	}
	b := out.Bytes()
	binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
	binary.LittleEndian.PutUint16(b[2:], value_length)
	if text {
		binary.LittleEndian.PutUint16(b[4:], 1)
	}
	return b
}

func utf16z(s string) []byte {
	var out bytes.Buffer
	for _, u := range utf16.Encode([]rune(s)) {
		_ = binary.Write(&out, binary.LittleEndian, u)
	}
	out.Write([]byte{0, 0})
	return out.Bytes()
}
