//go:build windows && !payload

package main

import "io/fs"

// payload_source is empty in a build without the payload tag -- the one
// `go build ./...` and CI produce -- and install refuses with the way to
// build a real setup.
func payload_source() fs.FS { return nil }
