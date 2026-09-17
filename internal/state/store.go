package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// current_version is the only Version this build writes and the highest one
// it will read.
const current_version = 1

// Err_corrupt means the file exists but did not parse. The store does not
// rename, delete or repair it -- deciding what to do with a corrupt state is
// core's job, and this repository's rule is that nothing is deleted.
var Err_corrupt = errors.New("state: file is corrupt")

// Err_future_version means the file parsed but names a Version higher than
// this build understands. Returning it instead of a partly decoded State
// stops a downgrade from silently reinterpreting a newer file.
var Err_future_version = errors.New("state: file is a version this build does not understand")

// Store reads and writes one state file. It is safe for concurrent use: the
// service reads and writes this from more than one goroutine.
type Store struct {
	path  string
	mutex sync.Mutex
	clock func() time.Time
}

// Open opens the store at path. It creates the containing directory but does
// not read the file -- a missing file is not an error until something asks
// to Load it.
func Open(path string) (*Store, error) {
	return Open_with_clock(path, time.Now)
}

// Open_with_clock is Open with an injected clock, so a test can assert the
// exact Updated_at stamp a Save produces instead of tolerating "close enough
// to now".
func Open_with_clock(path string, clock func() time.Time) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state: creating %s: %w", dir, err)
	}
	return &Store{path: path, clock: clock}, nil
}

// Load reads the state file. A missing file is not an error and is not
// distinguishable from "nothing has happened yet": both return the zero
// State.
func (s *Store) Load() (State, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("state: reading %s: %w", s.path, err)
	}

	// The version is checked before the rest of the file is decoded, so a
	// newer file with fields this build does not know about is refused
	// outright rather than partly understood.
	var versioned struct {
		Version int
	}
	if err := json.Unmarshal(raw, &versioned); err != nil {
		return State{}, Err_corrupt
	}
	if versioned.Version > current_version {
		return State{}, Err_future_version
	}

	var decoded State
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return State{}, Err_corrupt
	}
	return decoded, nil
}

// Save writes state to disk atomically: a temporary file in the same
// directory, synced, then renamed over the target. A machine that loses
// power mid-write comes back to the old file or the new one, never to half
// of one.
func (s *Store) Save(new_state State) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	new_state.Version = current_version
	new_state.Updated_at = s.clock()

	data, err := json.MarshalIndent(new_state, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encoding state: %w", err)
	}
	return atomic_write(s.path, data)
}

// atomic_write is the write-temp-sync-rename sequence Save relies on for its
// crash safety.
func atomic_write(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("state: creating temporary file: %w", err)
	}
	tmp_path := tmp.Name()
	defer os.Remove(tmp_path) // no-op once the rename below has succeeded

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("state: writing temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("state: syncing temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: closing temporary file: %w", err)
	}
	if err := os.Rename(tmp_path, path); err != nil {
		return fmt.Errorf("state: renaming temporary file into place: %w", err)
	}
	return nil
}
