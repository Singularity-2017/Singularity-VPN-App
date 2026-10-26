// Package statistics keeps local aggregate connection measurements. No session,
// account, profile, endpoint or network identifiers enter its persistent record.
package statistics

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Snapshot contains lifetime totals since Started_at, not the current session.
// Reconnections include every successful switching -> connected rebuild; Switches
// counts only explicit switch_completed events and is a subset of those rebuilds.
// Probe_samples counts distinct timed monitor samples, not individual TCP attempts.
type Snapshot struct {
	Started_at               time.Time `json:"started_at"`
	Total_connected_seconds  float64   `json:"total_connected_seconds"`
	Connections              uint64    `json:"connections"`
	Disconnects              uint64    `json:"disconnects"`
	Losses                   uint64    `json:"losses"`
	Reconnections            uint64    `json:"reconnections"`
	Switches                 uint64    `json:"switches"`
	Failed_connections       uint64    `json:"failed_connections"`
	Failed_reconnections     uint64    `json:"failed_reconnections"`
	Probe_samples            uint64    `json:"probe_samples"`
	Blips                    uint64    `json:"blips"`
	Degradations             uint64    `json:"degradations"`
	Average_rtt_ms           *float64  `json:"average_rtt_ms,omitempty"`
	Average_loss_percent     *float64  `json:"average_loss_percent,omitempty"`
	Udp_samples              uint64    `json:"udp_samples"`
	Average_udp_rtt_ms       *float64  `json:"average_udp_rtt_ms,omitempty"`
	Average_udp_loss_percent *float64  `json:"average_udp_loss_percent,omitempty"`
}

type record struct {
	Version               int       `json:"version"`
	Started_at            time.Time `json:"started_at"`
	Connected_nanoseconds int64     `json:"connected_nanoseconds"`
	Connections           uint64    `json:"connections"`
	Disconnects           uint64    `json:"disconnects"`
	Losses                uint64    `json:"losses"`
	Reconnections         uint64    `json:"reconnections"`
	Switches              uint64    `json:"switches"`
	Failed_connections    uint64    `json:"failed_connections"`
	Failed_reconnections  uint64    `json:"failed_reconnections"`
	Probe_samples         uint64    `json:"probe_samples"`
	Blips                 uint64    `json:"blips"`
	Degradations          uint64    `json:"degradations"`
	Rtt_sum_ms            float64   `json:"rtt_sum_ms"`
	Loss_sum_percent      float64   `json:"loss_sum_percent"`
	Udp_samples           uint64    `json:"udp_samples"`
	Udp_answered_samples  uint64    `json:"udp_answered_samples"`
	Udp_rtt_sum_ms        float64   `json:"udp_rtt_sum_ms"`
	Udp_loss_sum_percent  float64   `json:"udp_loss_sum_percent"`
}

// Store separates short in-memory updates from serialized disk writes. The
// caller supplies an already protected directory; this package never creates it.
// Active state and timestamps are deliberately not restored after a restart.
type Store struct {
	mu          sync.Mutex
	save_mu     sync.Mutex
	path        string
	now         func() time.Time
	data        record
	state       string
	changed_at  time.Time
	last_sample time.Time
	last_udp    time.Time
}

// Open treats only a missing file as a new history. Corrupt, truncated, invalid
// and unsupported records return an error and are never replaced with zeros.
func Open(path string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	s := &Store{path: path, now: now, state: "disconnected", changed_at: now()}
	s.data = record{Version: 1, Started_at: s.changed_at}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("statistics: opening record: %w", err)
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 64*1024))
	decoder.DisallowUnknownFields()
	var data record
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("statistics: decoding record: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("statistics: trailing record data")
	}
	if data.Version != 1 || data.Started_at.IsZero() || data.Connected_nanoseconds < 0 ||
		!finite_nonnegative(data.Rtt_sum_ms) || !finite_nonnegative(data.Loss_sum_percent) ||
		data.Loss_sum_percent > float64(data.Probe_samples)*100 ||
		(data.Probe_samples == 0 && data.Rtt_sum_ms != 0) ||
		(data.Probe_samples > 0 && data.Rtt_sum_ms == 0) {
		return nil, errors.New("statistics: invalid or unsupported record")
	}
	s.data = data
	return s, nil
}

func finite_nonnegative(value float64) bool {
	return value >= 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func connected(state string) bool { return state == "connected" || state == "degraded" }

// Transition accepts ordered state_changed events. Degradation and recovery
// within a session do not count as new connections. Cancellation is not failure.
func (s *Store) Transition(from, to string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.Before(s.changed_at) || s.state == to {
		return
	}
	s.accrue(at)
	s.state, s.changed_at = to, at
	switch {
	case from == "connecting" && to == "connected":
		s.data.Connections++
	case from == "switching" && to == "connected":
		s.data.Reconnections++
	case from == "disconnecting" && to == "disconnected":
		s.data.Disconnects++
	case from == "connecting" && to == "failed":
		s.data.Failed_connections++
	case from == "switching" && to == "failed":
		s.data.Failed_reconnections++
	}
}

func (s *Store) accrue(at time.Time) {
	if connected(s.state) && at.After(s.changed_at) {
		s.data.Connected_nanoseconds += int64(at.Sub(s.changed_at))
	}
}

func (s *Store) Lost()     { s.mu.Lock(); s.data.Losses++; s.mu.Unlock() }
func (s *Store) Switched() { s.mu.Lock(); s.data.Switches++; s.mu.Unlock() }

// Blip counts one bad sample the re-check did not confirm; Degraded counts a
// confirmed degradation.
func (s *Store) Blip()     { s.mu.Lock(); s.data.Blips++; s.mu.Unlock() }
func (s *Store) Degraded() { s.mu.Lock(); s.data.Degradations++; s.mu.Unlock() }

// Observe_sample records only a distinct, timed, valid measurement. Missing RTT
// means the default loss value is also unproven, so neither average changes.
// The manager calls this synchronously when it publishes a measured sample.
func (s *Store) Observe_sample(at time.Time, rtt time.Duration, loss float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.IsZero() || !at.After(s.last_sample) || at.After(s.now()) || rtt <= 0 ||
		!finite_nonnegative(loss) || loss > 1 {
		return
	}
	s.last_sample = at
	s.data.Probe_samples++
	s.data.Rtt_sum_ms += float64(rtt) / float64(time.Millisecond)
	s.data.Loss_sum_percent += loss * 100
}

// Observe_udp records one probe's UDP burst: its loss always, its round
// trip only when something came back. Like Observe_sample it takes each
// timed burst once.
func (s *Store) Observe_udp(at time.Time, rtt time.Duration, loss float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.IsZero() || !at.After(s.last_udp) || at.After(s.now()) || rtt < 0 ||
		!finite_nonnegative(loss) || loss > 1 {
		return
	}
	s.last_udp = at
	s.data.Udp_samples++
	s.data.Udp_loss_sum_percent += loss * 100
	if rtt > 0 {
		s.data.Udp_answered_samples++
		s.data.Udp_rtt_sum_ms += float64(rtt) / float64(time.Millisecond)
	}
}

func (s *Store) current() record {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.data
	if now := s.now(); connected(s.state) && now.After(s.changed_at) {
		data.Connected_nanoseconds += int64(now.Sub(s.changed_at))
	}
	return data
}

func (s *Store) Snapshot() Snapshot {
	d := s.current()
	result := Snapshot{Started_at: d.Started_at, Total_connected_seconds: float64(d.Connected_nanoseconds) / float64(time.Second),
		Connections: d.Connections, Disconnects: d.Disconnects, Losses: d.Losses,
		Reconnections: d.Reconnections, Switches: d.Switches, Failed_connections: d.Failed_connections,
		Failed_reconnections: d.Failed_reconnections, Probe_samples: d.Probe_samples,
		Blips: d.Blips, Degradations: d.Degradations}
	if d.Probe_samples > 0 {
		rtt, loss := d.Rtt_sum_ms/float64(d.Probe_samples), d.Loss_sum_percent/float64(d.Probe_samples)
		result.Average_rtt_ms, result.Average_loss_percent = &rtt, &loss
	}
	result.Udp_samples = d.Udp_samples
	if d.Udp_samples > 0 {
		loss := d.Udp_loss_sum_percent / float64(d.Udp_samples)
		result.Average_udp_loss_percent = &loss
	}
	if d.Udp_answered_samples > 0 {
		rtt := d.Udp_rtt_sum_ms / float64(d.Udp_answered_samples)
		result.Average_udp_rtt_ms = &rtt
	}
	return result
}

// Finish closes only the duration interval; it invents no disconnection event.
func (s *Store) Finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accrue(s.now())
	s.state = "disconnected"
}

// Save checkpoints a projection without moving the live interval start, so a
// queued transition with an earlier timestamp can still be accounted exactly.
// A crash loses only work since the last successful checkpoint; offline time is
// never reconstructed. Failed writes preserve the previous complete record.
func (s *Store) Save() error {
	s.save_mu.Lock()
	defer s.save_mu.Unlock()
	data, err := json.Marshal(s.current())
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".statistics-*.tmp")
	if err != nil {
		return fmt.Errorf("statistics: creating checkpoint: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return replace_file(f.Name(), s.path)
}
