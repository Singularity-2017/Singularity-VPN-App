package main

import (
	"fmt"
	"os"
	"sync"
)

// max_log_bytes is where service.log rolls over.
const max_log_bytes = 8 << 20

// capped_writer writes to a file and rolls it over once it passes its cap:
// service.log becomes service.log.1, replacing any previous .1, and a new
// service.log is started. One generation is kept on purpose -- this is a
// desktop machine, and a VPN service that fills somebody's disk with its
// own diagnostics has caused a worse problem than the one it was recording.
// WINDOWS_CLIENT.md 31 wants logs there to diagnose from; this keeps them
// there without letting them grow.
//
// A rollover that fails degrades to carrying on in the current file, past
// the cap: losing the log is worse than a log that is too long.
type capped_writer struct {
	mu   sync.Mutex
	path string
	max  int64
	file *os.File
	size int64
}

func open_capped_writer(path string, max int64) (*capped_writer, error) {
	w := &capped_writer{path: path, max: max}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *capped_writer) open() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening the log %s: %w", w.path, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("reading the size of the log %s: %w", w.path, err)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

// Write rolls over before a write that would cross the cap, never in the
// middle of one, so a log line is never split across the two files.
func (w *capped_writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.size > 0 && w.size+int64(len(p)) > w.max {
		w.roll()
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// roll never leaves w without an open file: every way out of it either
// swapped the new file in or reopened the old one.
func (w *capped_writer) roll() {
	w.file.Close()
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		// Still the same file on disk; carry on appending to it.
		if reopen_err := w.open(); reopen_err != nil {
			w.file, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		}
		return
	}
	if err := w.open(); err != nil {
		w.file, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		w.size = 0
	}
}

func (w *capped_writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}
