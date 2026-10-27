package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"singularity-vpn-app/internal/buildinfo"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/journal"
)

// A problem report is the person's own words plus two attachments: a
// diagnostics bundle and the tail of the service log. They are assembled
// first, as a draft the person is shown, and sent only when they say so --
// so what is shown is exactly what goes, and what goes is kept here, not
// rebuilt at the moment of sending.
const (
	// diagnostics_schema is the version of the bundle's shape. The control
	// plane reads connection.* and network_context_id; the rest is for the
	// person or the agent who reads the report.
	diagnostics_schema = 1
	// diagnostics_max_bytes bounds the bundle. The journal is what gives
	// way, oldest first: it is the only part that grows.
	diagnostics_max_bytes = 64 << 10
	// diagnostics_journal_window is how far back the bundle's journal
	// reaches.
	diagnostics_journal_window = 2 * time.Hour
	// network_summary_timeout bounds reading the adapters: a draft is
	// asked for by a person who is waiting.
	network_summary_timeout = 5 * time.Second

	// draft_lifetime is how long a draft can be sent after it was made;
	// what the person was shown should not be sent hours later.
	draft_lifetime = 15 * time.Minute
	// max_drafts is how many drafts are held; opening a fifth drops the
	// oldest.
	max_drafts = 4
)

// err_draft_expired is what sending a draft that is gone says.
var err_draft_expired = errors.New("the report draft expired; open it again")

// problem_connection is what the control plane reads to match a report with
// the connection it observed.
type problem_connection struct {
	State      string `json:"state"`
	Node_id    string `json:"node_id,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	Mode       string `json:"mode,omitempty"` // full, selective or games, as observations name it
	Session_id string `json:"session_id,omitempty"`
}

// problem_diagnostics is the bundle. Fields that are not known are left out:
// a version the build did not supply is not printed, nor is a network
// context the collector does not have.
type problem_diagnostics struct {
	Schema             int                 `json:"schema"`
	Created_at         time.Time           `json:"created_at"`
	App_version        string              `json:"app_version,omitempty"`
	Os                 string              `json:"os,omitempty"`
	Network_context_id string              `json:"network_context_id,omitempty"`
	Connection         problem_connection  `json:"connection"`
	Status             ipc.Status_result   `json:"status"`
	Failures           *ipc.Failure_signal `json:"failures,omitempty"`
	Strategy           *ipc.Strategy       `json:"strategy,omitempty"`
	Tunnel_mode        string              `json:"tunnel_mode,omitempty"`
	Network            json.RawMessage     `json:"network,omitempty"`
	Apps               []ipc.App_use       `json:"apps"`
	Journal            []journal.Entry     `json:"journal"`
}

// problem_draft_item is what is held for a draft.
type problem_draft_item struct {
	diagnostics json.RawMessage
	log         string
	created     time.Time
}

// problem_drafts holds the drafts in memory only: they are never written to
// disk, and a restart of the service drops them.
type problem_drafts struct {
	mu    sync.Mutex
	items map[string]problem_draft_item
}

// expire drops the drafts older than draft_lifetime; the caller holds mu.
func (d *problem_drafts) expire(now time.Time) {
	for id, item := range d.items {
		if now.Sub(item.created) > draft_lifetime {
			delete(d.items, id)
		}
	}
}

// put keeps a draft, and drops the oldest if that makes more than max_drafts.
func (d *problem_drafts) put(id string, item problem_draft_item, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.items == nil {
		d.items = make(map[string]problem_draft_item)
	}
	d.expire(now)
	d.items[id] = item
	for len(d.items) > max_drafts {
		oldest, oldest_at := "", time.Time{}
		for id, item := range d.items {
			if oldest == "" || item.created.Before(oldest_at) {
				oldest, oldest_at = id, item.created
			}
		}
		delete(d.items, oldest)
	}
}

// get finds a draft that has not expired.
func (d *problem_drafts) get(id string, now time.Time) (problem_draft_item, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expire(now)
	item, ok := d.items[id]
	return item, ok
}

// take removes a draft and hands it over: whoever has it is the only one who
// can send it, so two sends of one draft cannot both go out. A send that fails
// puts it back with put.
func (d *problem_drafts) take(id string, now time.Time) (problem_draft_item, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expire(now)
	item, ok := d.items[id]
	delete(d.items, id)
	return item, ok
}

// new_draft_id is 32 hex characters from the system's random source: the id
// is the only thing that lets a caller send a draft, so it is not guessable.
func new_draft_id() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("core: making a draft id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// Problem_draft answers Command_problem_draft: it assembles the diagnostics
// and the log tail a problem report will carry, keeps them under an id, and
// tells the window what they are. Nothing leaves the machine here.
func (a *Application) Problem_draft(ctx context.Context, params ipc.Problem_draft_params) (ipc.Problem_draft, error) {
	if err := params.Validate(); err != nil {
		return ipc.Problem_draft{}, err
	}
	status, err := a.Status(ctx)
	if err != nil {
		return ipc.Problem_draft{}, err
	}
	now := a.now()

	// The failure signal is the report's own field; the status copy inside
	// the bundle would only say it twice.
	failures := status.Failures
	status.Failures = nil
	diagnostics := problem_diagnostics{
		Schema:      diagnostics_schema,
		Created_at:  now,
		App_version: buildinfo.Version,
		Os:          a.os_version,
		Status:      status,
		Failures:    failures,
		Apps:        slices.Clone(params.Apps),
		Journal:     []journal.Entry{},
	}
	if diagnostics.Apps == nil {
		diagnostics.Apps = []ipc.App_use{}
	}
	diagnostics.Network_context_id = a.evidence.Context_id(ctx)

	// The stored choices are read for the report, not for the report to
	// fail on: a state file that cannot be read is itself a finding.
	if strategy, err := a.Strategy(ctx); err == nil {
		diagnostics.Strategy = &strategy
	} else {
		a.logger.Warn("problem report: the strategy could not be read", "error", err.Error())
	}
	if tunnel, err := a.Tunnel_mode(ctx); err == nil {
		diagnostics.Tunnel_mode = tunnel.Mode
	} else {
		a.logger.Warn("problem report: the tunnel mode could not be read", "error", err.Error())
	}

	if a.journal != nil {
		cutoff := now.Add(-diagnostics_journal_window)
		for _, entry := range a.journal.List() {
			if entry.At.Before(cutoff) {
				continue
			}
			entry.Message = redact_log_line(entry.Message)
			entry.Attrs = redact_attrs(entry.Attrs)
			diagnostics.Journal = append(diagnostics.Journal, entry)
		}
	}

	diagnostics.Connection = problem_connection{
		State:      status.State,
		Node_id:    status.Node_id,
		Protocol:   status.Protocol,
		Session_id: status.Session_id,
	}
	// A connection that never came up has no session, but the journal still
	// names the node and protocol it was trying.
	if diagnostics.Connection.Node_id == "" {
		diagnostics.Connection.Node_id, diagnostics.Connection.Protocol = latest_target(diagnostics.Journal)
	}
	// The mode is what an observation made now would carry, session or not.
	if mode, err := a.manager.Observation_mode(); err == nil {
		diagnostics.Connection.Mode = mode
	} else {
		a.logger.Warn("problem report: the mode could not be read", "error", err.Error())
	}

	if a.network_summary != nil {
		if raw := a.describe_network(ctx); raw != nil {
			diagnostics.Network = raw
		}
	}

	raw, err := fit_diagnostics(diagnostics, diagnostics_max_bytes)
	if err != nil {
		return ipc.Problem_draft{}, err
	}

	var log string
	var log_from time.Time
	if a.log_path != "" {
		log, log_from, err = read_log_tail(a.log_path, now)
		if err != nil {
			// The report is still worth sending without it.
			a.logger.Warn("problem report: the log could not be read", "error", err.Error())
			log, log_from = "", time.Time{}
		}
	}

	id, err := new_draft_id()
	if err != nil {
		return ipc.Problem_draft{}, err
	}
	a.drafts.put(id, problem_draft_item{diagnostics: raw, log: log, created: now}, now)
	// The log is in the draft as it is held, so what the person reads is
	// what is sent.
	draft := ipc.Problem_draft{Id: id, Diagnostics: raw, Log_bytes: len(log), Log: log}
	if !log_from.IsZero() {
		draft.Log_from = &log_from
	}
	return draft, nil
}

// latest_target is the node and protocol named by the newest journal entry
// that names both: the target of the latest attempt, or of the connection that
// was lost. Entries are oldest first, as the journal keeps them.
func latest_target(entries []journal.Entry) (node_id, protocol string) {
	for i := len(entries) - 1; i >= 0; i-- {
		if node, proto := entries[i].Attrs["node_id"], entries[i].Attrs["protocol"]; node != "" && proto != "" {
			return node, proto
		}
	}
	return "", ""
}

// describe_network asks for the network summary and waits for it no longer
// than network_summary_timeout or ctx: the call is a system call that can
// stall, and a draft is asked for by a person who is waiting. The call is
// left to finish on its own if it does not; its answer is dropped. nil is no
// summary.
func (a *Application) describe_network(ctx context.Context) json.RawMessage {
	ctx, cancel := context.WithTimeout(ctx, network_summary_timeout)
	defer cancel()
	type answer struct {
		summary any
		err     error
	}
	answered := make(chan answer, 1) // buffered: a late answer must not strand the goroutine
	go func() {
		summary, err := a.network_summary(ctx)
		answered <- answer{summary, err}
	}()
	select {
	case <-ctx.Done():
		a.logger.Warn("problem report: the network summary did not answer in time")
		return nil
	case result := <-answered:
		if result.err != nil {
			a.logger.Warn("problem report: the network could not be described", "error", result.err.Error())
			return nil
		}
		raw, err := json.Marshal(result.summary)
		if err != nil || string(raw) == "null" {
			return nil
		}
		return raw
	}
}

// fit_diagnostics serializes the bundle in at most limit bytes by dropping
// the journal's oldest entries; the newest say the most about what is wrong
// now. A bundle too large without any journal is refused, not cut.
func fit_diagnostics(diagnostics problem_diagnostics, limit int) (json.RawMessage, error) {
	entries := diagnostics.Journal
	diagnostics.Journal = []journal.Entry{}
	base, err := json.Marshal(diagnostics)
	if err != nil {
		return nil, fmt.Errorf("core: encoding the diagnostics: %w", err)
	}
	if len(base) > limit {
		return nil, fmt.Errorf("core: the diagnostics are %d bytes without the journal, over the %d allowed", len(base), limit)
	}

	// Each entry adds its own length and, after the first, a comma.
	room, used, keep := limit-len(base), 0, 0
	for i := len(entries) - 1; i >= 0; i-- {
		encoded, err := json.Marshal(entries[i])
		if err != nil {
			return nil, fmt.Errorf("core: encoding the journal: %w", err)
		}
		cost := len(encoded)
		if keep > 0 {
			cost++
		}
		if used+cost > room {
			break
		}
		used += cost
		keep++
	}
	diagnostics.Journal = append([]journal.Entry{}, entries[len(entries)-keep:]...)
	out, err := json.Marshal(diagnostics)
	// The sum above is exact; this only guards a change to how entries encode.
	for err == nil && len(out) > limit && len(diagnostics.Journal) > 0 {
		diagnostics.Journal = diagnostics.Journal[1:]
		out, err = json.Marshal(diagnostics)
	}
	if err != nil {
		return nil, fmt.Errorf("core: encoding the diagnostics: %w", err)
	}
	return out, nil
}
