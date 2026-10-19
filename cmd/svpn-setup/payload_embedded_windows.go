//go:build windows && payload

package main

import (
	"embed"
	"io/fs"
)

// The payload directory is filled by scripts/build.ps1 -Setup right before
// this build and is git-ignored; the tag keeps every other build of the
// repository from needing it.
//
//go:embed payload
var payload_fs embed.FS

func payload_source() fs.FS {
	sub, err := fs.Sub(payload_fs, "payload")
	if err != nil {
		return nil
	}
	return sub
}
