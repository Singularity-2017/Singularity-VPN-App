package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Schema is the manifest format this build reads.
const Schema = 1

// Max_manifest_bytes bounds a manifest download; a real one is a few
// hundred bytes, and a channel is never trusted to be honest about size.
const Max_manifest_bytes = 64 << 10

// Max_setup_bytes bounds what a manifest may claim the setup weighs.
const Max_setup_bytes = 512 << 20

// Artifact is one downloadable file of a release.
type Artifact struct {
	File   string `json:"file"`
	Sha256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest describes a release. It is published as manifest.json beside a
// detached signature, manifest.json.sig, so that the bytes verified are the
// bytes parsed and no canonical form is needed.
type Manifest struct {
	Schema          int       `json:"schema"`
	Version         string    `json:"version"`
	Released_at     time.Time `json:"released_at"`
	Minimum_version string    `json:"minimum_version,omitempty"`
	Urgent          bool      `json:"urgent,omitempty"`
	Setup           Artifact  `json:"setup"`

	// Mirrors are base URLs serving manifest.json, manifest.json.sig and
	// the artifacts by name. Being inside the signed document is what lets
	// a blocked mirror be replaced by installing the next version.
	Mirrors []string `json:"mirrors,omitempty"`

	// Control_planes are the control plane's current base URLs. Signed
	// with the release, they move installations to a re-addressed control
	// plane; see docs/adr/0001-control-plane-address.md. Absent means
	// unchanged, never none.
	Control_planes []string `json:"control_planes,omitempty"`
}

// Err_bad_signature is a manifest whose signature no trusted key accepts.
var Err_bad_signature = errors.New("update: the manifest signature does not verify")

// Verify checks signature -- base64 text, as the control plane serves it --
// over exactly manifest, against keys. Any one key accepting it is enough:
// the second key is the offline backup.
func Verify(manifest, signature []byte, keys []ed25519.PublicKey) error {
	if len(keys) == 0 {
		return errors.New("update: no trusted key is compiled into this build")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return Err_bad_signature
	}
	for _, key := range keys {
		if ed25519.Verify(key, manifest, raw) {
			return nil
		}
	}
	return Err_bad_signature
}

// Parse decodes and validates a manifest that has already been verified.
// Unknown fields are allowed: later releases add fields, and a build must
// still read what it understands.
func Parse(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("update: manifest does not parse: %w", err)
	}
	if m.Schema != Schema {
		return Manifest{}, fmt.Errorf("update: manifest schema %d, this build reads %d", m.Schema, Schema)
	}
	if _, ok := Parse_version(m.Version); !ok {
		return Manifest{}, fmt.Errorf("update: manifest version %q is not a release version", m.Version)
	}
	if m.Minimum_version != "" {
		if _, ok := Parse_version(m.Minimum_version); !ok {
			return Manifest{}, fmt.Errorf("update: minimum version %q is not a release version", m.Minimum_version)
		}
	}
	if !File_name_ok(m.Setup.File) || !strings.HasPrefix(m.Setup.File, "SingularityVPN-setup-") || !strings.HasSuffix(m.Setup.File, ".exe") {
		// The setup's name is fixed to its pattern so a manifest cannot
		// name a file the update directory already uses for something
		// else -- update.json, swap.log, svpn-swap.exe -- or a device.
		return Manifest{}, fmt.Errorf("update: setup file name %q is not SingularityVPN-setup-*.exe", m.Setup.File)
	}
	if len(m.Setup.Sha256) != 64 || strings.ToLower(m.Setup.Sha256) != m.Setup.Sha256 {
		return Manifest{}, fmt.Errorf("update: setup sha256 %q is not 64 lowercase hex digits", m.Setup.Sha256)
	}
	if _, err := hex.DecodeString(m.Setup.Sha256); err != nil {
		return Manifest{}, fmt.Errorf("update: setup sha256 is not hex")
	}
	if m.Setup.Size <= 0 || m.Setup.Size > Max_setup_bytes {
		return Manifest{}, fmt.Errorf("update: setup size %d is out of range", m.Setup.Size)
	}
	if len(m.Mirrors) > 16 {
		return Manifest{}, fmt.Errorf("update: %d mirrors, at most 16", len(m.Mirrors))
	}
	for _, mirror := range m.Mirrors {
		if err := mirror_ok(mirror); err != nil {
			return Manifest{}, err
		}
	}
	return m, nil
}

// File_name_ok is the same rule the control plane applies to what it
// serves: a plain file name, no path, nothing that walks out of a directory
// or confuses a URL.
func File_name_ok(name string) bool {
	if name == "" || len(name) > 128 || strings.HasPrefix(name, ".") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// mirror_ok admits an absolute https URL. Content is verified whatever the
// transport, but a mirror is chosen by us and costs nothing to serve over
// TLS, which keeps what is being downloaded from whoever watches the link.
func mirror_ok(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("update: mirror %q is not an absolute https URL", raw)
	}
	return nil
}

// Version is a release version, major.minor.patch.
type Version [3]int

// Parse_version accepts "1.2.3" and "v1.2.3" and nothing else. What git
// describe makes of an untagged or dirty tree ("v1.2.3-4-gabcdef-dirty") is
// not a release, and a build carrying it does not update itself: there is
// no honest answer to whether it is older than a release.
func Parse_version(s string) (Version, bool) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var v Version
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return Version{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, false
		}
		v[i] = n
	}
	return v, true
}

// Less reports whether v is older than other.
func (v Version) Less(other Version) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}
