//go:build !windows

package statistics

import "os"

func replace_file(source, destination string) error { return os.Rename(source, destination) }
