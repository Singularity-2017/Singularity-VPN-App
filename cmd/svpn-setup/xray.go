package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"singularity-vpn-app/internal/xraybundle"
)

// The shared embedded pin is the authority; the installed manifest is informational.
var xray_pin = xraybundle.Manifest_bytes()

func verify_xray_payload(files map[string][]byte) error {
	var pin struct {
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(xray_pin, &pin); err != nil {
		return fmt.Errorf("reading Xray pin: %w", err)
	}
	if len(pin.Files) == 0 {
		return fmt.Errorf("empty Xray pin")
	}
	if !bytes.Equal(files["xray/manifest.json"], xray_pin) {
		return fmt.Errorf("Xray manifest differs from embedded pin")
	}
	for name, expected := range pin.Files {
		digest := sha256.Sum256(files["xray/"+name])
		if hex.EncodeToString(digest[:]) != expected {
			return fmt.Errorf("Xray payload hash mismatch: %s", name)
		}
	}
	return nil
}
