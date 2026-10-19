package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

// trusted_keys are the public halves of the release signing keys, made
// with `svpn-release keygen`. The first signs releases; the second is the
// backup, kept offline, for the day the first is lost or must be retired.
// Public keys are not secret; what matters is that they are compiled in,
// so no channel can substitute its own.
var trusted_keys = []string{
	"I7y66g4mxflhvdXtLLyYve0qwiyarwGqPzsyRCIY7+0=", // primary, 2026-10-19
	"2s5UKROj+iA74E2H/m5IY5rtBbA6agncoa/2Spg4tNU=", // backup, 2026-10-19
}

// Trusted_keys decodes trusted_keys.
func Trusted_keys() ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(trusted_keys))
	for _, encoded := range trusted_keys {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("update: trusted key %q is not an Ed25519 public key", encoded)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys, nil
}
