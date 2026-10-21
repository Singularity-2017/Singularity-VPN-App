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
	"log/slog"
	"regexp"
	"slices"
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
}

// New makes a journal that keeps the newest size entries.
func New(size int) *Journal {
	return &Journal{entries: make([]Entry, size)}
}

func (j *Journal) add(e Entry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries[j.next] = e
	j.next = (j.next + 1) % len(j.entries)
	if j.next == 0 {
		j.full = true
	}
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
	"service starting":                                         true,
	"connect requested":                                        true,
	"disconnect requested":                                     true,
	"control plane recommendation":                             true,
	"using an offered alternative this build speaks":           true,
	"using the offered protocol that worked last on this node": true,
	"verification failed":                                      true,
	"reconnected at start: the machine was left connected":     true,
	"retry on the same node failed to connect":                 true,
	"retry on the same node failed verification":               true,
	"network changed; moving the endpoint route failed":        true,
	"strategy set":                                             true,
	"games set":                                                true,
	"tunnel mode set":                                          true,
	"game router started":                                      true,
	"game router stopped":                                      true,
	"game router: capture stopped; games go direct until the next connect": true,
	"event": true,
}

// Events are the event kinds kept from "event" records; the others repeat
// what an operation above already says, or say nothing a person can use.
var Events = map[string]bool{
	"connection_established":  true,
	"connection_lost":         true,
	"connection_degraded":     true,
	"switch_started":          true,
	"switch_completed":        true,
	"switch_failed":           true,
	"better_option_available": false,
}

// ipv4 finds an address in a value. The window is no place for the control
// plane's address or a node's; a log file read by an administrator is.
var ipv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

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
			e.Attrs[a.Key] = ipv4.ReplaceAllString(a.Value.Resolve().String(), "…")
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

// keep reports a record the journal holds: a warning or an error always,
// an informational record when it is an operation.
func keep(r slog.Record) bool {
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
