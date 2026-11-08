// Package xraybundle holds the single compiled trust authority shared by the
// installer and native runtime. The installed manifest is informational only.
package xraybundle

import (
	_ "embed"
	"encoding/json"
)

//go:embed pins.json
var manifest []byte

func Manifest_bytes() []byte { return append([]byte(nil), manifest...) }
func File_hash(name string) string {
	var pin struct {
		Files map[string]string `json:"files"`
	}
	if json.Unmarshal(manifest, &pin) != nil {
		return ""
	}
	return pin.Files[name]
}
