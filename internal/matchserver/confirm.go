package matchserver

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Answer is what the player said about a named server.
type Answer string

const (
	// Answer_yes: the game shows the same server.
	Answer_yes Answer = "yes"
	// Answer_no: it shows another, given in Shown when the player typed it.
	Answer_no Answer = "no"
	// Answer_dismiss: the player would rather not say. It counts as
	// neither right nor wrong.
	Answer_dismiss Answer = "dismiss"
)

// Verdict is what an answer makes of a detection.
type Verdict string

const (
	Verdict_right     Verdict = "right"
	Verdict_wrong     Verdict = "wrong"
	Verdict_dismissed Verdict = "dismissed"
)

// Confirmation is one answer, with the detection it was about as it stood
// when the player answered.
type Confirmation struct {
	At     time.Time `json:"at"`
	Game   string    `json:"game"`
	Region string    `json:"region,omitempty"`
	// Detected is the server the player was shown, Shown the address the
	// game itself shows: the same on a "yes", the corrected one on a "no"
	// that gave one, absent otherwise.
	Detected  netip.AddrPort `json:"detected"`
	Shown     netip.Addr     `json:"shown,omitzero"`
	Verdict   Verdict        `json:"verdict"`
	Detection Candidate      `json:"detection"`
}

// Parse_shown reads the address a game shows, with or without its port.
// Empty is no address.
func Parse_shown(s string) (netip.Addr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, nil
	}
	if a, err := netip.ParseAddrPort(s); err == nil {
		return a.Addr(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("matchserver: %q is not an address", s)
	}
	return a, nil
}

// Confirm turns an answer about c into a confirmation. A "no" naming the
// same address the detector did is a "yes": the game's window shows no
// port, and the address is what is being checked.
func Confirm(c Candidate, answer Answer, shown netip.Addr, at time.Time) (Confirmation, error) {
	confirmation := Confirmation{At: at, Game: c.Game, Region: c.Region, Detected: c.Server, Detection: c}
	switch answer {
	case Answer_yes:
		confirmation.Shown, confirmation.Verdict = c.Server.Addr(), Verdict_right
	case Answer_no:
		confirmation.Shown, confirmation.Verdict = shown, Verdict_wrong
		if shown == c.Server.Addr() {
			confirmation.Verdict = Verdict_right
		}
	case Answer_dismiss:
		confirmation.Verdict = Verdict_dismissed
	default:
		return Confirmation{}, fmt.Errorf("matchserver: answer %q is not yes, no or dismiss", answer)
	}
	return confirmation, nil
}

// Tally is how often the detector was right, among the matches a player
// confirmed one way or the other. Dismissals are in neither number.
type Tally struct {
	Right     int
	Confirmed int
}

func (t *Tally) count(c Confirmation) {
	switch c.Verdict {
	case Verdict_right:
		t.Right++
		t.Confirmed++
	case Verdict_wrong:
		t.Confirmed++
	}
}

// File names of the store, in its directory.
const (
	detections_file    = "match_detections.jsonl"
	confirmations_file = "match_confirmations.jsonl"
)

// Store keeps every detection and every confirmation on this machine, one
// JSON line each, and the tally per game. Nothing in it leaves the machine.
type Store struct {
	mu    sync.Mutex
	dir   string // "" keeps nothing on disk
	tally map[string]Tally
}

// Open_store reads the confirmations already in dir to rebuild the tally.
// An empty dir keeps everything in memory, for tests and for a service
// with nowhere to write. A line that does not parse is skipped: one bad
// write must not lose the rest.
func Open_store(dir string) (*Store, error) {
	s := &Store{dir: dir, tally: map[string]Tally{}}
	if dir == "" {
		return s, nil
	}
	f, err := os.Open(filepath.Join(dir, confirmations_file))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("matchserver: reading confirmations: %w", err)
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for lines.Scan() {
		var c Confirmation
		if json.Unmarshal(lines.Bytes(), &c) != nil {
			continue
		}
		t := s.tally[c.Game]
		t.count(c)
		s.tally[c.Game] = t
	}
	return s, lines.Err()
}

// detected records a match's detection.
func (s *Store) detected(c Candidate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.append(detections_file, c)
}

// confirmed records an answer and counts it.
func (s *Store) confirmed(c Confirmation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tally[c.Game]
	t.count(c)
	s.tally[c.Game] = t
	return s.append(confirmations_file, c)
}

// Tally is the tally for one game.
func (s *Store) Tally(game string) Tally {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tally[game]
}

func (s *Store) append(name string, record any) error {
	if s.dir == "" {
		return nil
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("matchserver: opening %s: %w", name, err)
	}
	_, err = f.Write(append(line, '\n'))
	if close_err := f.Close(); err == nil {
		err = close_err
	}
	return err
}
