package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"
)

const (
	// analysis_due_after is how long a measurement keeps a network from being
	// measured again on its own: one automatic run per network per day.
	analysis_due_after = 24 * time.Hour

	// analysis_networks_keep is how long a network is remembered after its
	// last measurement. A network not seen for a month is as good as new.
	analysis_networks_keep = 30 * 24 * time.Hour

	// analysis_networks_max bounds how many networks are remembered; the
	// oldest go first.
	analysis_networks_max = 64

	// analysis_networks_limit is far above what analysis_networks_max keys of
	// a hash and a time take; a bigger file is not ours.
	analysis_networks_limit = 64 * 1024

	// network_key_max_length bounds a key read from the file. A key is a hex
	// HMAC, 64 characters.
	network_key_max_length = 128

	// network_key_timeout bounds one reading of the network key.
	network_key_timeout = 3 * time.Second
)

var (
	// err_analysis_unknown_network says no automatic run is possible because
	// the network under the machine cannot be told. A manual run still can.
	err_analysis_unknown_network = errors.New("core: the network under the machine is not known")

	// err_analysis_not_due says this network was measured within the day.
	err_analysis_not_due = errors.New("core: this network was measured within the last day")

	// err_analysis_spaced says an automatic run started too recently, whatever
	// came of it: a run that measured nothing is not recorded, and without a
	// floor a flapping link would fetch the catalog and probe back to back.
	err_analysis_spaced = errors.New("core: an automatic analysis started too recently")
)

// analysis_due is the rule for a run nobody asked for: it is due when the
// network is known and has no successful measurement in the last
// analysis_due_after. A measurement dated in the future (the clock moved
// back) does not count, so the clock cannot silence the measuring for longer
// than a day. It returns nil when a run is due, and says why not otherwise.
//
// The other two parts of the rule -- a signed-in device and a connection that
// is down -- hold for a manual run too, so begin_analysis checks them for
// both, before it comes here.
func analysis_due(measured map[string]time.Time, network string, now time.Time) error {
	if network == "" {
		return err_analysis_unknown_network
	}
	if at, ok := measured[network]; ok {
		if age := now.Sub(at); age >= 0 && age < analysis_due_after {
			return err_analysis_not_due
		}
	}
	return nil
}

// prune_networks removes from measured everything not worth keeping at now:
// an empty or oversized key, a time that is zero, in the future or older than
// analysis_networks_keep -- and then, past analysis_networks_max, the oldest.
func prune_networks(measured map[string]time.Time, now time.Time) {
	for key, at := range measured {
		if key == "" || len(key) > network_key_max_length || at.IsZero() || at.After(now) || now.Sub(at) > analysis_networks_keep {
			delete(measured, key)
		}
	}
	if len(measured) <= analysis_networks_max {
		return
	}
	keys := make([]string, 0, len(measured))
	for key := range measured {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if !measured[keys[i]].Equal(measured[keys[j]]) {
			return measured[keys[i]].After(measured[keys[j]])
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys[analysis_networks_max:] {
		delete(measured, key)
	}
}

// current_network is the key of the network under the machine, or "" when it
// cannot be told: no source wired, or the source failed. The key is a salted
// hash; the material behind it never leaves the source.
func (a *Application) current_network(ctx context.Context) string {
	if a.network_key == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, network_key_timeout)
	defer cancel()
	key, err := a.network_key(ctx)
	if err != nil || len(key) > network_key_max_length {
		return ""
	}
	return key
}

// restore_networks_locked loads the per-network measurement times once. A
// missing file is a first run; an unreadable or corrupt one is logged by
// category only and starts the memory afresh, which at worst costs one extra
// measurement per network.
func (a *Application) restore_networks_locked() {
	c := &a.analysis
	if c.networks_loaded {
		return
	}
	c.networks_loaded = true
	c.networks = map[string]time.Time{}
	if a.analysis_networks_path == "" {
		return
	}
	data, err := read_small_file(a.analysis_networks_path, analysis_networks_limit)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		a.logger.Warn("analysis networks file unavailable")
		return
	}
	var stored map[string]time.Time
	if json.Unmarshal(data, &stored) != nil {
		a.logger.Warn("analysis networks file invalid")
		return
	}
	prune_networks(stored, a.now())
	if stored != nil {
		c.networks = stored
	}
}

// record_network_locked notes a successful measurement of network at at, and
// writes the file. An empty network -- the run began where nothing could be
// told about the network -- is not recorded.
func (a *Application) record_network_locked(network string, at time.Time) {
	if network == "" {
		return
	}
	a.restore_networks_locked()
	c := &a.analysis
	c.networks[network] = at
	prune_networks(c.networks, a.now())
	if a.analysis_networks_path == "" {
		return
	}
	data, err := json.Marshal(c.networks)
	if err == nil {
		err = write_small_file(a.analysis_networks_path, ".analysis-networks-*.tmp", data)
	}
	if err != nil {
		a.logger.Warn("analysis networks file could not be saved")
	}
}
