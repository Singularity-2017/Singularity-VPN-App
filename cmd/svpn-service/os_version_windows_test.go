//go:build windows

package main

import (
	"regexp"
	"testing"
)

func Test_os_version_is_major_minor_build(t *testing.T) {
	got := os_version()
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(got) {
		t.Fatalf("os_version() = %q, want major.minor.build", got)
	}
}
