// Package journal keeps the service's recent operations for the window to
// show: what was asked, what was tried, what failed and why, in the words
// the log already uses. It is fed by a slog.Handler wrapped around the
// service's own, so every operation the log records reaches the person
// without a second call at each site.
//
// Only the messages in the operations list are kept: the log also carries
// churn -- a game's ports opening and closing, packet counts every ten
// seconds -- which would push every operation out of a short buffer. A
// message renamed in the code drops out of the journal silently, which is
// why the list sits here, next to a test that names each one.
package journal

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Entry is one operation.
type Entry struct {
	At      time.Time         `json:"at"`
	Level   string            `json:"level"` // "info", "warn" or "error"
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// Journal is a fixed-size ring of the newest entries. It is safe for
// concurrent use.
type Journal struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	full    bool

	// path, when set, is where the journal outlives the service: written
	// flush_delay after a change, and at once by Flush.
	path      string
	flush_set bool
}

// New makes a journal that keeps the newest size entries.
func New(size int) *Journal {
	return &Journal{entries: make([]Entry, size)}
}

// flush_delay gathers the entries of one burst into one write.
const flush_delay = 2 * time.Second

// Stopped and ended are the last words of a service run. A journal read back
// that ends in neither belongs to a run that never got to say them: the
// machine lost power, or the service crashed.
const (
	Stopped            = "service stopped"
	Stopped_with_error = "service stopped with an error"
	Ended_unstopped    = "service ended without stopping"
)

// Open makes a journal kept in the file at path, starting from what the
// last run left there. A file that is missing or unreadable starts empty:
// the journal is a convenience, never a reason for the service not to run.
func Open(path string, size int) *Journal {
	j := New(size)
	j.path = path
	raw, err := os.ReadFile(path)
	if err != nil {
		return j
	}
	var saved []Entry
	if json.Unmarshal(raw, &saved) != nil {
		return j
	}
	for _, e := range saved {
		j.put(e)
	}
	if n := len(saved); n > 0 && saved[n-1].Message != Stopped && saved[n-1].Message != Stopped_with_error {
		// When it ended is not known; the last thing it said is when it
		// was last heard from.
		j.put(Entry{At: saved[n-1].At, Level: "warn", Message: Ended_unstopped})
	}
	return j
}

func (j *Journal) put(e Entry) {
	j.entries[j.next] = e
	j.next = (j.next + 1) % len(j.entries)
	if j.next == 0 {
		j.full = true
	}
}

func (j *Journal) add(e Entry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.put(e)
	if j.path != "" && !j.flush_set {
		j.flush_set = true
		time.AfterFunc(flush_delay, func() { _ = j.Flush() })
	}
}

// Flush writes the journal to its file now. A journal without one has
// nothing to do.
func (j *Journal) Flush() error {
	if j.path == "" {
		return nil
	}
	entries := j.List()
	j.mu.Lock()
	j.flush_set = false
	j.mu.Unlock()
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(j.path), ".journal-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), j.path)
}

// List is every entry kept, oldest first.
func (j *Journal) List() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.full {
		return slices.Clone(j.entries[:j.next])
	}
	return append(slices.Clone(j.entries[j.next:]), j.entries[:j.next]...)
}

// Operations are the log messages the journal keeps.
var Operations = map[string]bool{
	"service starting":             true,
	Stopped:                        true,
	"connect requested":            true,
	"disconnect requested":         true,
	"control plane recommendation": true,
	"using an offered alternative this build speaks":                  true,
	"using the offered protocol that worked last on this node":        true,
	"leaving aside what this network reported or keeps losing":        true,
	"the connection keeps dropping; moving to another protocol":       true,
	"the connection keeps dropping after a move for drops; staying":   true,
	"the connection keeps dropping; there is nothing else to move to": true,
	"verification failed": true,
	"reconnected at start: the machine was left connected": true,
	"retry on the same node failed to connect":             true,
	"retry on the same node failed verification":           true,
	"network changed; moving the endpoint route failed":    true,
	"strategy set":        true,
	"games set":           true,
	"tunnel mode set":     true,
	"game router started": true,
	"game router stopped": true,
	"game router: capture stopped; games go direct until the next connect": true,
	"event": true,
	"the control plane moved; its address came from a signed manifest": true,
}

// Events are the event kinds kept from "event" records; the others repeat
// what an operation above already says, or say nothing a person can use.
var Events = map[string]bool{
	"profile_changed":         true,
	"network_changed":         true,
	"connection_established":  true,
	"connection_lost":         true,
	"connection_degraded":     true,
	"connection_blip":         true,
	"switch_started":          true,
	"switch_completed":        true,
	"switch_failed":           true,
	"better_option_available": false,
}

// ipv4 finds an address in a value. The window is no place for the control
// plane's address or a node's; a log file read by an administrator is.
var ipv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// ipv6_candidate finds a run of characters that may be an IPv6 address. A
// time of day or a MAC address looks like one until netip is asked, so the
// regexp only proposes.
var ipv6_candidate = regexp.MustCompile(`[0-9A-Fa-f:]*:[0-9A-Fa-f:]*`)

// Mask_addresses replaces every IPv4 and IPv6 address in s with an ellipsis.
// What goes into the journal passes through it, and so does a log tail sent
// with a problem report, which is read by people the machine's owner does not
// know. A run that is not an address, or is part of a longer word, stays.
func Mask_addresses(s string) string {
	s = ipv4.ReplaceAllString(s, "…")
	var out strings.Builder
	last := 0
	for _, m := range ipv6_candidate.FindAllStringIndex(s, -1) {
		start, end := m[0], m[1]
		if start > 0 && is_word_byte(s[start-1]) || end < len(s) && is_word_byte(s[end]) {
			continue
		}
		if ip, err := netip.ParseAddr(s[start:end]); err != nil || !ip.Is6() {
			continue
		}
		out.WriteString(s[last:start])
		out.WriteString("…")
		last = end
	}
	out.WriteString(s[last:])
	return out.String()
}

func is_word_byte(b byte) bool {
	return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

// Handler passes every record on to next and keeps the operations in j.
func Handler(next slog.Handler, j *Journal) slog.Handler {
	return &handler{next: next, journal: j}
}

type handler struct {
	next    slog.Handler
	journal *Journal
	attrs   []slog.Attr
}

func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if keep(r) {
		e := Entry{At: r.Time, Level: level_name(r.Level), Message: r.Message, Attrs: map[string]string{}}
		put := func(a slog.Attr) bool {
			if a.Key == "at" && r.Message == "event" {
				return true // the record's own time says it
			}
			e.Attrs[a.Key] = Mask_addresses(a.Value.Resolve().String())
			return true
		}
		for _, a := range h.attrs {
			put(a)
		}
		r.Attrs(put)
		if len(e.Attrs) == 0 {
			e.Attrs = nil
		}
		h.journal.add(e)
	}
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{next: h.next.WithAttrs(attrs), journal: h.journal, attrs: append(slices.Clone(h.attrs), attrs...)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{next: h.next.WithGroup(name), journal: h.journal, attrs: h.attrs}
}

// keep reports a record the journal holds: operational warnings and errors,
// and informational records when they are operations. Probe diagnostics stay
// in the file log; connection events report their operational consequence.
func keep(r slog.Record) bool {
	switch r.Message {
	case "connection probe failed", "probe target measurement failed":
		return false
	}
	if r.Level >= slog.LevelWarn {
		return true
	}
	if !Operations[r.Message] {
		return false
	}
	if r.Message != "event" {
		return true
	}
	kept := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "kind" {
			kept = Events[a.Value.String()]
			return false
		}
		return true
	})
	return kept
}

func level_name(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	default:
		return "info"
	}
}
