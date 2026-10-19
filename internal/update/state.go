package update

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const state_file = "update.json"

// State is what the updater remembers between runs, in its own private
// directory. It is shared with svpn-swap, which records a failed version
// here so the same release is not applied again straight away.
type State struct {
	// Mirrors come from the newest manifest that verified, so the mirror
	// list a blocked mirror is replaced from is itself signed.
	Mirrors []string `json:"mirrors,omitempty"`

	// Highest_version is the newest release this installation has ever
	// verified. A manifest older than it is a replay and is ignored.
	Highest_version string `json:"highest_version,omitempty"`

	Staged *Staged `json:"staged,omitempty"`

	// Failed_version was applied and reversed at Failed_at because the
	// new service did not prove itself healthy. It is tried again after a
	// while -- sooner if this installation is below the minimum.
	Failed_version string    `json:"failed_version,omitempty"`
	Failed_at      time.Time `json:"failed_at,omitzero"`
}

// Staged is a downloaded, verified setup waiting to be applied.
type Staged struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	Sha256  string `json:"sha256"`
	Urgent  bool   `json:"urgent,omitempty"`

	// Launched_at is when a swap was started for it. While a swap may
	// still be running another is not launched: two setups at once would
	// fight over the same files and the same service.
	Launched_at time.Time `json:"launched_at,omitzero"`
}

// Swap_window is the longest a launched swap can take: a setup run capped
// at ten minutes, a minute of health checks, and a rollback setup capped
// at ten more, with some to spare.
const Swap_window = 25 * time.Minute

// Load_state reads dir's state. None yet is an empty state, and so is a
// corrupt one -- a power cut mid-write on a disk that lied about it --
// after it is set aside and logged, because an update state that blocks
// every future update is worse than one that has forgotten a mirror list.
//
// A file that exists but cannot be read is an error, not an empty state:
// every caller writes the state back, and writing back an empty one would
// erase the replay floor and the failure mark. Reads are retried briefly,
// since a scanner or svpn-swap holding the file is the usual cause.
func Load_state(dir string, logger *slog.Logger) (State, error) {
	path := filepath.Join(dir, state_file)
	var data []byte
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if data, err = os.ReadFile(path); err == nil || errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		os.Rename(path, path+".corrupt")
		if logger != nil {
			logger.Warn("update state corrupt; set aside, starting from none", "err", err)
		}
		return State{}, nil
	}
	return s, nil
}

// Save_state writes dir's state through a temporary file flushed to disk
// before it replaces the old one.
func Save_state(dir string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, state_file)
	temp := path + ".tmp"
	if err := write_synced(temp, data); err != nil {
		os.Remove(temp)
		return err
	}
	// The rename fails with a sharing violation while anything else has
	// update.json open -- Go opens files without FILE_SHARE_DELETE.
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

// Clear_staged is what svpn-swap calls once the new version is healthy.
func Clear_staged(dir string) error {
	s, err := Load_state(dir, nil)
	if err != nil {
		return err
	}
	if s.Staged != nil {
		os.Remove(s.Staged.Path)
		s.Staged = nil
	}
	return Save_state(dir, s)
}

// Record_failure is what svpn-swap calls after reversing version.
func Record_failure(dir, version string, at time.Time) error {
	s, err := Load_state(dir, nil)
	if err != nil {
		return err
	}
	s.Failed_version = version
	s.Failed_at = at
	if s.Staged != nil {
		os.Remove(s.Staged.Path)
		s.Staged = nil
	}
	return Save_state(dir, s)
}
