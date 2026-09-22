package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func Test_the_log_rolls_over_once_it_passes_the_cap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	w, err := open_capped_writer(path, 32)
	if err != nil {
		t.Fatalf("open_capped_writer: %v", err)
	}
	defer w.Close()

	first := "first line, twenty-something\n"
	second := "second line crosses the cap\n"
	w.Write([]byte(first))
	w.Write([]byte(second))

	if got := read(t, path+".1"); got != first {
		t.Fatalf("service.log.1 = %q, want the content from before the rollover, %q", got, first)
	}
	if got := read(t, path); got != second {
		t.Fatalf("service.log = %q, want only what came after the rollover, %q", got, second)
	}
}

func Test_a_second_rollover_replaces_the_old_generation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	w, err := open_capped_writer(path, 16)
	if err != nil {
		t.Fatalf("open_capped_writer: %v", err)
	}
	defer w.Close()

	for _, line := range []string{"aaaaaaaaaaaa\n", "bbbbbbbbbbbb\n", "cccccccccccc\n"} {
		w.Write([]byte(line))
	}

	if got := read(t, path+".1"); got != "bbbbbbbbbbbb\n" {
		t.Fatalf("service.log.1 = %q, want the second generation only", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".2") {
			t.Fatalf("found %s: one old generation is kept, not a growing series", entry.Name())
		}
	}
}

// Test_a_reopened_log_counts_what_is_already_there guards the service
// restarting over a log that is nearly full: starting the count from zero
// would let the file grow to twice the cap before its first rollover, and
// again after every restart.
func Test_a_reopened_log_counts_what_is_already_there(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte("already here, from before a restart\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := open_capped_writer(path, 40)
	if err != nil {
		t.Fatalf("open_capped_writer: %v", err)
	}
	defer w.Close()

	w.Write([]byte("new line\n"))

	if got := read(t, path); got != "new line\n" {
		t.Fatalf("service.log = %q, want a rollover before the new line", got)
	}
}

// Test_a_log_that_could_not_reopen_recovers_on_the_next_write guards the
// one way a rollover can leave the writer without a file: the next write
// must reopen it, not dereference nothing inside a LocalSystem service.
func Test_a_log_that_could_not_reopen_recovers_on_the_next_write(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	w, err := open_capped_writer(path, 1<<20)
	if err != nil {
		t.Fatalf("open_capped_writer: %v", err)
	}
	defer w.Close()

	w.file.Close()
	w.file = nil
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatalf("Write after a failed reopen = %v, want the log reopened", err)
	}
	if got := read(t, path); got != "after\n" {
		t.Fatalf("service.log = %q, want the line written after reopening", got)
	}
}
