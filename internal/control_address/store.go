// Package control_address owns the one place the service keeps the control
// plane's current address. The compiled address is only the first value:
// once a signed manifest has named the control plane, its list replaces the
// compiled one for good -- see docs/adr/0001-control-plane-address.md.
package control_address

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// File_name is the store's file inside the service's private data directory.
const File_name = "control-plane.json"

// Max_addresses bounds a list; a manifest naming more is malformed.
const Max_addresses = 4

// Max_future is how far ahead of this machine's clock a statement may be
// dated. Further is a typo or a wrong clock where it was signed, and taken
// once it would lock the list against every later release.
const Max_future = 24 * time.Hour

// record is what the file holds: the list and the released_at of the
// newest signed manifest seen, the bar any later list must clear. An
// empty list is a bar alone: the compiled address is still in use.
type record struct {
	Addresses   []string  `json:"addresses"`
	Released_at time.Time `json:"released_at"`
}

// Store is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	dir       string
	compiled  string
	saved     *record
	listeners []func([]string)
	logger    *slog.Logger
	now       func() time.Time
}

// Open reads what dir holds. A file that is missing, does not parse or
// holds an invalid list means nothing is stored -- never no control plane.
func Open(dir, compiled string, logger *slog.Logger) *Store {
	s := &Store{dir: dir, compiled: compiled, logger: logger, now: time.Now}
	data, err := os.ReadFile(filepath.Join(dir, File_name))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		logger.Warn("control plane address file unreadable, using the compiled address", "err", err)
	default:
		var r record
		if err := json.Unmarshal(data, &r); err != nil {
			logger.Warn("control plane address file does not parse, using the compiled address", "err", err)
			break
		}
		list, err := Normalize(r.Addresses)
		if err != nil || r.Released_at.IsZero() {
			logger.Warn("control plane address file holds no valid list, using the compiled address", "err", err)
			break
		}
		r.Addresses = list
		s.saved = &r
	}
	return s
}

// Current is the list to use, first address first.
func (s *Store) Current() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.current_locked()...)
}

// On_change registers f, called with the new list after each change.
func (s *Store) On_change(f func([]string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, f)
}

// Offer takes list from a verified manifest released at released_at. It is
// taken only when strictly newer than the newest statement seen. An empty
// list keeps the addresses but still raises the bar, so an older manifest
// naming an address we gave up cannot follow it. changed reports a
// different list.
func (s *Store) Offer(list []string, released_at time.Time) (changed bool, err error) {
	if released_at.After(s.now().Add(Max_future)) {
		return false, fmt.Errorf("control address: the manifest is dated %s, more than a day ahead", released_at.Format(time.RFC3339))
	}
	list, err = Normalize(list)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	if s.saved != nil && !released_at.After(s.saved.Released_at) {
		s.mu.Unlock()
		return false, nil
	}
	if len(list) == 0 && s.saved != nil {
		list = s.saved.Addresses
	}
	changed = len(list) > 0 && !equal(list, s.current_locked())
	next := &record{Addresses: list, Released_at: released_at}
	if err := s.write(next); err != nil {
		s.mu.Unlock()
		return false, err
	}
	s.saved = next
	listeners := append([]func([]string){}, s.listeners...)
	s.mu.Unlock()
	if changed {
		for _, f := range listeners {
			f(append([]string(nil), list...))
		}
	}
	return changed, nil
}

func (s *Store) current_locked() []string {
	if s.saved == nil || len(s.saved.Addresses) == 0 {
		return []string{s.compiled}
	}
	return s.saved.Addresses
}

func (s *Store) write(r *record) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, File_name)
	temp := path + ".tmp"
	// Synced before the rename: a file lost to a power cut would mean the
	// compiled address again, the one a move may have handed to a stranger.
	if err := write_synced(temp, data); err != nil {
		os.Remove(temp)
		return err
	}
	// The rename fails with a sharing violation while anything else has
	// the file open -- Go opens files without FILE_SHARE_DELETE.
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.Rename(temp, path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	os.Remove(temp)
	return err
}

func write_synced(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// Normalize validates a list of control plane base URLs and returns it
// without trailing slashes and duplicates. One bad entry refuses the list:
// a signed list that is partly wrong is not partly trusted.
func Normalize(list []string) ([]string, error) {
	if len(list) > Max_addresses {
		return nil, fmt.Errorf("control address: %d addresses, at most %d", len(list), Max_addresses)
	}
	var out []string
	for _, raw := range list {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
			return nil, fmt.Errorf("control address: %q is not an absolute http(s) URL without query or user", raw)
		}
		clean := strings.TrimRight(raw, "/")
		if !contains(out, clean) {
			out = append(out, clean)
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, have := range list {
		if have == s {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
