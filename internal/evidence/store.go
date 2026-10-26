// Package evidence retains contextual lifecycle facts independently of delivery.
package evidence

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"singularity-vpn-app/internal/api"
	"sync"
	"time"
)

const Max_events = 512
const Retention = 30 * 24 * time.Hour
const Summary_window = 7 * 24 * time.Hour

type Record struct {
	Context      api.Network_context        `json:"context"`
	Account      string                     `json:"account"`
	Network      string                     `json:"network"`
	Observation  api.Connection_observation `json:"observation"`
	Rejected     string                     `json:"rejected,omitempty"`
	Acknowledged bool                       `json:"acknowledged"`
}
type Summary struct {
	Account    string                  `json:"account"`
	Context_id string                  `json:"context_id"`
	Evidence   api.Connection_evidence `json:"evidence"`
}
type disk_state struct {
	Version         int       `json:"version"`
	Salt            string    `json:"salt"`
	Records         []Record  `json:"records"`
	Summaries       []Summary `json:"summaries"`
	Dropped_pending uint64    `json:"dropped_pending"`
}
type Store struct {
	mu   sync.Mutex
	path string
	data disk_state
}

func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			if err = json.Unmarshal(data, &s.data); err != nil {
				return nil, fmt.Errorf("evidence: invalid store: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if s.data.Version > 1 {
		return nil, fmt.Errorf("evidence: unsupported store version")
	}
	if s.data.Salt == "" {
		var salt [32]byte
		if _, err := rand.Read(salt[:]); err != nil {
			return nil, err
		}
		s.data.Salt = hex.EncodeToString(salt[:])
	}
	s.data.Version = 1
	return s, nil
}

// Fingerprint is salted locally; physical addresses and interface identifiers never persist.
func (s *Store) Fingerprint(material string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	mac := hmac.New(sha256.New, []byte(s.data.Salt))
	mac.Write([]byte(material))
	return hex.EncodeToString(mac.Sum(nil))
}
func Account(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func New_id() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (s *Store) prune(now time.Time) {
	keep := s.data.Records[:0]
	for _, record := range s.data.Records {
		if now.Sub(record.Observation.Observed_at) > Retention {
			if !record.Acknowledged {
				s.data.Dropped_pending++
			}
			continue
		}
		keep = append(keep, record)
	}
	s.data.Records = keep
	for len(s.data.Records) > Max_events {
		index := 0
		for i, record := range s.data.Records {
			if record.Acknowledged {
				index = i
				break
			}
		}
		if !s.data.Records[index].Acknowledged {
			s.data.Dropped_pending++
		}
		s.data.Records = append(s.data.Records[:index], s.data.Records[index+1:]...)
	}
	summaries := s.data.Summaries[:0]
	for _, summary := range s.data.Summaries {
		if now.Sub(summary.Evidence.Observed_at) <= Summary_window {
			summaries = append(summaries, summary)
		}
	}
	s.data.Summaries = summaries
	if len(s.data.Summaries) > 256 {
		s.data.Summaries = s.data.Summaries[len(s.data.Summaries)-256:]
	}
}
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	data, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".evidence-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
func (s *Store) Add(record Record, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.data.Records {
		if existing.Observation.Observation_id == record.Observation.Observation_id {
			return nil
		}
	}
	s.data.Records = append(s.data.Records, record)
	s.prune(now)
	return s.save()
}
func (s *Store) Pending(account string, now time.Time) []api.Connection_observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	events := []api.Connection_observation{}
	context_id := ""
	for _, record := range s.data.Records {
		if record.Account == account && !record.Acknowledged && record.Rejected == "" && record.Observation.Context_id != "" {
			if context_id == "" {
				context_id = record.Observation.Context_id
			}
			if record.Observation.Context_id != context_id {
				continue
			}
			events = append(events, record.Observation)
			if len(events) >= 32 {
				break
			}
		}
	}
	return events
}
func (s *Store) Acknowledge(account string, ids []string, summaries []Summary, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ack := map[string]bool{}
	for _, id := range ids {
		ack[id] = true
	}
	for i := range s.data.Records {
		record := &s.data.Records[i]
		if record.Account == account && ack[record.Observation.Observation_id] {
			record.Acknowledged = true
		}
	}
	// Responses replace matching summary rows; their provenance remains intact.
	for _, incoming := range summaries {
		replaced := false
		for i, current := range s.data.Summaries {
			if current.Account == incoming.Account && current.Context_id == incoming.Context_id && current.Evidence.Source == incoming.Evidence.Source && current.Evidence.Node_id == incoming.Evidence.Node_id && current.Evidence.Protocol == incoming.Evidence.Protocol && current.Evidence.Mode == incoming.Evidence.Mode && current.Evidence.Profile_version == incoming.Evidence.Profile_version && current.Evidence.Scope == incoming.Evidence.Scope && current.Evidence.Country == incoming.Evidence.Country && current.Evidence.Asn == incoming.Evidence.Asn && current.Evidence.Region == incoming.Evidence.Region {
				s.data.Summaries[i] = incoming
				replaced = true
				break
			}
		}
		if !replaced {
			s.data.Summaries = append(s.data.Summaries, incoming)
		}
	}
	s.prune(now)
	return s.save()
}
func (s *Store) Snapshot() ([]Record, []Summary, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record{}, s.data.Records...), append([]Summary{}, s.data.Summaries...), s.data.Dropped_pending
}

func (s *Store) Context(account, id string) api.Network_context {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.data.Records {
		if record.Account == account && record.Observation.Context_id == id {
			return record.Context
		}
	}
	return api.Network_context{}
}

// Reject retains permanently refused events for local inspection without blocking new deliveries.
func (s *Store) Reject(account string, ids []string, reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rejected := map[string]bool{}
	for _, id := range ids {
		rejected[id] = true
	}
	for i := range s.data.Records {
		record := &s.data.Records[i]
		if record.Account == account && rejected[record.Observation.Observation_id] {
			record.Rejected = reason
		}
	}
	s.prune(now)
	return s.save()
}

type Local_summary struct {
	Network  string                  `json:"network"`
	Evidence api.Connection_evidence `json:"evidence"`
}

func (s *Store) Local_summaries(account, network string, now time.Time) []Local_summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	type key struct{ node, protocol, mode, version, scope, country, asn, region string }
	grouped := map[key]*api.Connection_evidence{}
	order := []key{}
	for _, record := range s.data.Records {
		event := record.Observation
		if record.Account != account || record.Network != network || now.Sub(event.Observed_at) > Summary_window || event.Observed_at.After(now) {
			continue
		}
		scope := record.Context.Scope
		if scope == "" {
			scope = "unknown"
		}
		k := key{event.Node_id, event.Protocol, event.Mode, event.Profile_version, scope, record.Context.Country, record.Context.Asn, record.Context.Region}
		row := grouped[k]
		if row == nil {
			row = &api.Connection_evidence{Source: "app", Node_id: k.node, Protocol: k.protocol, Mode: k.mode, Profile_version: k.version, Scope: k.scope, Country: k.country, Asn: k.asn, Region: k.region, Window_start: now.Add(-Summary_window), Window_end: now}
			grouped[k] = row
			order = append(order, k)
		}
		row.Observation_count++
		if event.Observed_at.After(row.Observed_at) {
			row.Observed_at = event.Observed_at
		}
		switch event.Kind {
		case "connection_established":
			row.Attempts++
			row.Established++
		case "connection_failed":
			row.Attempts++
			if scored_reason(event.Reason) {
				row.Failures++
			}
		case "connection_lost":
			if scored_reason(event.Reason) {
				row.Failures++
			}
		case "session_ended":
			row.Ended_sessions++
			if event.Duration_seconds != nil {
				if row.Active_seconds == nil {
					row.Active_seconds = new(float64)
				}
				*row.Active_seconds += *event.Duration_seconds
			}
		}
	}
	result := []Local_summary{}
	for _, k := range order {
		result = append(result, Local_summary{Network: network, Evidence: *grouped[k]})
	}
	return result
}
func scored_reason(reason string) bool {
	return reason == "transport_timeout" || reason == "transport_failure" || reason == "reachability_lost" || reason == "tunnel_stopped"
}
