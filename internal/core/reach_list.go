package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/ipc"
)

// The service list the analysis probes is the control plane's catalog, as the
// person edited it: minus the catalog entries they removed, plus the hosts they
// added. The edits live in reach-list.json beside the other service data; the
// catalog itself is never stored, so a host the control plane drops or adds
// follows without any migration.
//
// The four list commands (Services, Remove_service, Add_service,
// Restore_services) all need the catalog, because the list is the catalog
// plus the edits: with the control plane unreachable they fail with that
// error, and change nothing. That is one rule for all four, instead of a list
// that is half there and edits that apply to a list the person cannot see.

const (
	// reach_list_limit is far above what reach_added_max hosts and
	// reach_removed_max keys take; a bigger file is not ours.
	reach_list_limit = 256 * 1024

	// reach_added_max is how many hosts the person may add.
	reach_added_max = 30

	// reach_removed_max bounds how many removed catalog keys are kept.
	reach_removed_max = 512

	// reach_catalog_timeout bounds fetching the catalog for a list command.
	reach_catalog_timeout = 15 * time.Second

	// custom_key_prefix starts the key of an added host.
	custom_key_prefix = "custom:"

	default_service_port = 443
)

var _ ipc.Reach_list_service = (*Application)(nil)

// reach_added is a host the person added, as stored.
type reach_added struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// reach_edits is what the person changed about the catalog, as stored.
type reach_edits struct {
	Removed []string      `json:"removed"`
	Added   []reach_added `json:"added"`
}

// reach_list_state holds the edits in memory, read from the file on first use.
type reach_list_state struct {
	mu     sync.Mutex
	loaded bool
	edits  reach_edits
}

// custom_key is the key of an added host: the host and port say what it is,
// so adding it twice is the same entry.
func custom_key(host string, port int) string {
	return custom_key_prefix + host + ":" + strconv.Itoa(port)
}

func host_port(host string, port int) string {
	return strings.ToLower(host) + ":" + strconv.Itoa(port)
}

// parse_service_host reads what the person typed into a host and a port. It
// trims, drops a leading http:// or https:// and everything from the first
// slash, takes an optional :port (1 to 65535, 443 when absent), lowercases,
// and holds the host to a DNS host name. Anything else -- an address, a name
// without a dot, a character a name cannot have -- is ipc.Err_invalid_host.
func parse_service_host(raw string) (string, int, error) {
	text := strings.TrimSpace(raw)
	for _, scheme := range []string{"http://", "https://"} {
		if len(text) >= len(scheme) && strings.EqualFold(text[:len(scheme)], scheme) {
			text = text[len(scheme):]
			break
		}
	}
	if slash := strings.IndexByte(text, '/'); slash >= 0 {
		text = text[:slash]
	}
	host, port_text, has_port := strings.Cut(text, ":")
	host = strings.ToLower(host)
	port := default_service_port
	if has_port {
		parsed, ok := parse_port(port_text)
		if !ok {
			return "", 0, ipc.Err_invalid_host
		}
		port = parsed
	}
	if !valid_hostname(host) {
		return "", 0, ipc.Err_invalid_host
	}
	return host, port, nil
}

// parse_port reads a decimal port from 1 to 65535 and nothing else: no sign,
// no space, no leading garbage.
func parse_port(text string) (int, bool) {
	if text == "" || len(text) > 5 {
		return 0, false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

// valid_hostname holds a lowercase host to a DNS name with at least one dot:
// labels of 1 to 63 characters from a-z, 0-9 and '-', none starting or ending
// with '-', at most 253 characters in all. A last label of digits only is
// refused as well -- no top-level domain is, and what it would be is an IPv4
// address, which is not a service to be named.
func valid_hostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	// A top-level label never starts with a digit; one that does (127.0.0.0x1)
	// may be read by the resolver as a numeric address.
	last := labels[len(labels)-1]
	return last[0] < '0' || last[0] > '9'
}

// sanitize_reach_edits makes edits read from disk safe to use: duplicates,
// empty or oversized keys, hosts that no longer pass parse_service_host's
// rules and anything past the limits are dropped. The file is ours, but it is
// also a file a person can edit.
func sanitize_reach_edits(in reach_edits) reach_edits {
	out := reach_edits{Removed: []string{}, Added: []reach_added{}}
	seen := map[string]bool{}
	for _, key := range in.Removed {
		if key == "" || len(key) > 300 || seen[key] || len(out.Removed) >= reach_removed_max {
			continue
		}
		seen[key] = true
		out.Removed = append(out.Removed, key)
	}
	seen = map[string]bool{}
	for _, added := range in.Added {
		key := custom_key(added.Host, added.Port)
		if added.Host != strings.ToLower(added.Host) || !valid_hostname(added.Host) || added.Port < 1 || added.Port > 65535 || seen[key] || len(out.Added) >= reach_added_max {
			continue
		}
		seen[key] = true
		out.Added = append(out.Added, added)
	}
	return out
}

// copy_reach_edits returns edits that share nothing with in.
func copy_reach_edits(in reach_edits) reach_edits {
	return reach_edits{Removed: append([]string{}, in.Removed...), Added: append([]reach_added{}, in.Added...)}
}

// compose_reach_list builds the effective list from the catalog and the
// edits, in two forms that cannot disagree: the targets to probe, and the
// list to show. Catalog entries come first in the catalog's order, then the
// added hosts in the order they were added.
//
// A control -- a catalog entry with a country -- is never left out, whatever
// the edits say. A removed key that is no longer in the catalog counts for
// nothing. An added host the catalog now carries itself is left to the
// catalog's entry.
func compose_reach_list(catalog []api.Reach_target, edits reach_edits) ([]api.Reach_target, ipc.Services_result) {
	removed := make(map[string]bool, len(edits.Removed))
	for _, key := range edits.Removed {
		removed[key] = true
	}
	effective := make([]api.Reach_target, 0, len(catalog)+len(edits.Added))
	view := ipc.Services_result{Targets: make([]ipc.Service_target, 0, len(catalog)+len(edits.Added))}
	kept := make(map[string]bool, len(catalog))
	add := func(t api.Reach_target, popular bool) {
		label := t.Label
		if label == "" {
			label = t.Host
		}
		effective = append(effective, t)
		view.Targets = append(view.Targets, ipc.Service_target{
			Key: t.Key, Label: label, Host: t.Host, Port: t.Port, Popular: popular, Control: t.Country != "",
		})
	}
	for _, t := range catalog {
		if t.Country == "" && removed[t.Key] {
			view.Removed_popular++
			continue
		}
		kept[host_port(t.Host, t.Port)] = true
		add(t, true)
	}
	for _, added := range edits.Added {
		if kept[host_port(added.Host, added.Port)] {
			continue
		}
		add(api.Reach_target{Key: custom_key(added.Host, added.Port), Host: added.Host, Port: added.Port, Label: added.Host}, false)
	}
	return effective, view
}

// edit_remove removes the service with key: a catalog entry is added to the
// removed keys, an added host is dropped from the added ones. A control is
// ipc.Err_service_not_removable, and a key naming neither is
// ipc.Err_unknown_service. Removing what is already removed changes nothing.
func edit_remove(catalog []api.Reach_target, edits reach_edits, key string) (reach_edits, error) {
	next := copy_reach_edits(edits)
	for _, t := range catalog {
		if t.Key != key {
			continue
		}
		if t.Country != "" {
			return edits, ipc.Err_service_not_removable
		}
		for _, existing := range next.Removed {
			if existing == key {
				return next, nil
			}
		}
		next.Removed = append(next.Removed, key)
		return next, nil
	}
	for i, added := range next.Added {
		if custom_key(added.Host, added.Port) == key {
			next.Added = append(next.Added[:i], next.Added[i+1:]...)
			return next, nil
		}
	}
	return edits, ipc.Err_unknown_service
}

// edit_add adds the host the person typed. A host the catalog carries is a
// duplicate -- unless the person removed it, in which case adding it again
// puts it back instead of making a second entry. Past reach_added_max it is
// ipc.Err_too_many_services.
func edit_add(catalog []api.Reach_target, edits reach_edits, raw string) (reach_edits, error) {
	host, port, err := parse_service_host(raw)
	if err != nil {
		return edits, err
	}
	next := copy_reach_edits(edits)

	removed := make(map[string]bool, len(next.Removed))
	for _, key := range next.Removed {
		removed[key] = true
	}
	var restored []string
	for _, t := range catalog {
		if host_port(t.Host, t.Port) != host_port(host, port) {
			continue
		}
		if t.Country != "" || !removed[t.Key] {
			return edits, ipc.Err_duplicate_service
		}
		restored = append(restored, t.Key)
	}
	if len(restored) > 0 {
		kept := next.Removed[:0]
		for _, key := range next.Removed {
			put_back := false
			for _, r := range restored {
				put_back = put_back || r == key
			}
			if !put_back {
				kept = append(kept, key)
			}
		}
		next.Removed = kept
		return next, nil
	}

	for _, added := range next.Added {
		if host_port(added.Host, added.Port) == host_port(host, port) {
			return edits, ipc.Err_duplicate_service
		}
	}
	if len(next.Added) >= reach_added_max {
		return edits, ipc.Err_too_many_services
	}
	next.Added = append(next.Added, reach_added{Host: host, Port: port})
	return next, nil
}

// edit_restore forgets every removal and keeps what was added.
func edit_restore(_ []api.Reach_target, edits reach_edits) (reach_edits, error) {
	next := copy_reach_edits(edits)
	next.Removed = []string{}
	return next, nil
}

// load_reach_edits_locked reads the edits from the file once. A missing file
// is no edits; an unreadable or corrupt one is logged by category only and
// taken as none -- the person gets the catalog back, and loses nothing the
// catalog cannot give again but their added hosts.
func (a *Application) load_reach_edits_locked() {
	l := &a.reach_list
	if l.loaded {
		return
	}
	l.loaded = true
	l.edits = sanitize_reach_edits(reach_edits{})
	if a.reach_list_path == "" {
		return
	}
	data, err := read_small_file(a.reach_list_path, reach_list_limit)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		a.logger.Warn("service list file unavailable")
		return
	}
	var stored reach_edits
	if json.Unmarshal(data, &stored) != nil {
		a.logger.Warn("service list file invalid")
		return
	}
	l.edits = sanitize_reach_edits(stored)
}

// reach_edits_snapshot is the edits as they stand, for a reader that must not
// hold the lock.
func (a *Application) reach_edits_snapshot() reach_edits {
	l := &a.reach_list
	l.mu.Lock()
	defer l.mu.Unlock()
	a.load_reach_edits_locked()
	return copy_reach_edits(l.edits)
}

// reach_catalog fetches the catalog for a list command.
func (a *Application) reach_catalog(ctx context.Context) (api.Reach_catalog, error) {
	token, err := (&token_source{secrets: a.secrets}).Token(ctx)
	if err != nil {
		return api.Reach_catalog{}, map_error(err)
	}
	ctx, cancel := context.WithTimeout(ctx, reach_catalog_timeout)
	defer cancel()
	catalog, err := a.control_plane.Reach_targets(ctx, token)
	if err != nil {
		return api.Reach_catalog{}, map_error(err)
	}
	return catalog, nil
}

// Services answers Command_services: the effective list, and how many catalog
// entries are removed.
func (a *Application) Services(ctx context.Context) (ipc.Services_result, error) {
	catalog, err := a.reach_catalog(ctx)
	if err != nil {
		return ipc.Services_result{}, err
	}
	_, view := compose_reach_list(catalog.Targets, a.reach_edits_snapshot())
	return view, nil
}

// Remove_service answers Command_remove_service.
func (a *Application) Remove_service(ctx context.Context, params ipc.Remove_service_params) (ipc.Services_result, error) {
	return a.edit_reach_list(ctx, func(catalog []api.Reach_target, edits reach_edits) (reach_edits, error) {
		return edit_remove(catalog, edits, params.Key)
	})
}

// Add_service answers Command_add_service.
func (a *Application) Add_service(ctx context.Context, params ipc.Add_service_params) (ipc.Services_result, error) {
	return a.edit_reach_list(ctx, func(catalog []api.Reach_target, edits reach_edits) (reach_edits, error) {
		return edit_add(catalog, edits, params.Host)
	})
}

// Restore_services answers Command_restore_services: every removed catalog
// entry comes back, the added hosts stay.
func (a *Application) Restore_services(ctx context.Context) (ipc.Services_result, error) {
	return a.edit_reach_list(ctx, edit_restore)
}

// edit_reach_list applies one edit to the stored edits and answers with the
// list as it is then. The edit is written before it is kept in memory, so a
// write that fails leaves both as they were.
func (a *Application) edit_reach_list(ctx context.Context, edit func([]api.Reach_target, reach_edits) (reach_edits, error)) (ipc.Services_result, error) {
	catalog, err := a.reach_catalog(ctx)
	if err != nil {
		return ipc.Services_result{}, err
	}
	l := &a.reach_list
	l.mu.Lock()
	defer l.mu.Unlock()
	a.load_reach_edits_locked()
	next, err := edit(catalog.Targets, l.edits)
	if err != nil {
		return ipc.Services_result{}, err
	}
	if err := a.save_reach_edits_locked(next); err != nil {
		a.logger.Warn("service list file could not be saved")
		return ipc.Services_result{}, fmt.Errorf("core: saving the service list: %w", err)
	}
	l.edits = next
	_, view := compose_reach_list(catalog.Targets, next)
	return view, nil
}

// save_reach_edits_locked writes the edits atomically; with no path set, as in
// a test, they live in memory only.
func (a *Application) save_reach_edits_locked(edits reach_edits) error {
	if a.reach_list_path == "" {
		return nil
	}
	data, err := json.Marshal(edits)
	if err != nil {
		return err
	}
	return write_small_file(a.reach_list_path, ".reach-list-*.tmp", data)
}
