package core

import (
	"slices"
	"time"

	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/journal"
)

// The failure signal is how the window learns that a connection keeps failing
// for no reason the person can see, and offers to report a problem. It is read
// from the journal, which already holds every failure in the words the log
// uses, so nothing new is recorded for it.
const (
	// failure_window is how far back failures count: older ones are history,
	// not a connection failing now.
	failure_window = 30 * time.Minute
	// network_change_grace is how long after a network change a failure is
	// put down to the change. A tunnel dropping as the Wi-Fi roams is the
	// network's doing, and a report about it would only be noise.
	network_change_grace = 60 * time.Second
	// network_change_lead is how long before a network_changed entry a
	// failure is put down to the change too. The entry is journaled after the
	// change has settled, a few seconds on, and the connection drops as it
	// begins.
	network_change_lead = 10 * time.Second
	// failure_threshold is the score at which the signal appears: a blip
	// weighs 1, a loss or a failed connect 2, so a loss and a blip, or three
	// blips, are enough and one blip is not.
	failure_threshold = 3

	failure_weight_blip = 1
	failure_weight_hard = 2
)

// failure_messages are the journal messages that say an operation failed.
var failure_messages = map[string]bool{
	"verification failed":                        true,
	"retry on the same node failed to connect":   true,
	"retry on the same node failed verification": true,
	journal.Stopped_with_error:                   true,
}

// failure_events are the "event" kinds that say the connection was lost.
var failure_events = map[string]bool{
	"connection_lost": true,
	"switch_failed":   true,
}

// failure_class says which kind of failure an entry reports, "" for none.
type failure_class string

const (
	failure_blip   failure_class = "blip"
	failure_lost   failure_class = "lost"
	failure_failed failure_class = "failed"
)

func classify_failure(entry journal.Entry) failure_class {
	if entry.Message == "event" {
		switch kind := entry.Attrs["kind"]; {
		case kind == "connection_blip":
			return failure_blip
		case failure_events[kind]:
			return failure_lost
		}
		return ""
	}
	if failure_messages[entry.Message] {
		return failure_failed
	}
	return ""
}

// failure_signal counts the failures in the last failure_window, leaving out
// what something else explains: a failure from network_change_lead before to
// network_change_grace after a network_changed event, and anything after the
// person's "disconnect requested" until their next "connect requested". It is
// nil unless the score reaches failure_threshold.
//
// entries is read oldest first, whole: whether the person had disconnected
// before the window began decides whether its first failures count.
func failure_signal(entries []journal.Entry, now time.Time) *ipc.Failure_signal {
	ordered := slices.Clone(entries)
	slices.SortStableFunc(ordered, func(a, b journal.Entry) int { return a.At.Compare(b.At) })

	// The changes are known before the failures are read: one explains the
	// failures that came before it as well as those after.
	var network_changes []time.Time
	for _, entry := range ordered {
		if entry.Message == "event" && entry.Attrs["kind"] == "network_changed" {
			network_changes = append(network_changes, entry.At)
		}
	}
	explained_by_network := func(at time.Time) bool {
		return slices.ContainsFunc(network_changes, func(change time.Time) bool {
			return !at.Before(change.Add(-network_change_lead)) && !at.After(change.Add(network_change_grace))
		})
	}

	cutoff := now.Add(-failure_window)
	var (
		signal       ipc.Failure_signal
		disconnected bool
		score        int
	)
	for _, entry := range ordered {
		switch {
		case entry.Message == "disconnect requested":
			disconnected = true
			continue
		case entry.Message == "connect requested":
			disconnected = false
			continue
		}

		class := classify_failure(entry)
		if class == "" || disconnected || entry.At.Before(cutoff) || entry.At.After(now) {
			continue
		}
		if explained_by_network(entry.At) {
			continue
		}

		signal.Count++
		switch class {
		case failure_blip:
			signal.Blips++
			score += failure_weight_blip
		case failure_lost:
			signal.Lost++
			score += failure_weight_hard
		case failure_failed:
			signal.Failed++
			score += failure_weight_hard
		}
		if signal.First.IsZero() {
			signal.First = entry.At
		}
		signal.Last = entry.At
	}
	if score < failure_threshold {
		return nil
	}
	return &signal
}
