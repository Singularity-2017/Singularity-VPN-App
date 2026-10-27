//go:build !windows

package core

import "os"

// open_shared opens a file for reading; elsewhere nothing stops a reader
// sharing a file with its writer.
func open_shared(path string) (*os.File, error) { return os.Open(path) }
