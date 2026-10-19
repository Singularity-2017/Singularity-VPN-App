//go:build !windows

package main

import "os"

// protect relies on the mode bits, which on Unix are the whole story.
func protect(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.Chmod(path, 0o700)
	}
	return os.Chmod(path, 0o600)
}
