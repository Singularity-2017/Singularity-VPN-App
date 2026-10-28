package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Config is everything an Updater needs.
type Config struct {
	Control_planes  func() []string // current base URLs; nil or empty skips that channel
	Fallback        string          // a mirror compiled into the build; empty means none
	Current_version string          // buildinfo.Version
	Keys            []ed25519.PublicKey
	Dir             string // private directory for state and staged files
	Http_client     *http.Client

	// Idle reports that no tunnel is carrying traffic, which is when a
	// non-urgent update may be applied.
	Idle func() bool

	// Apply hands a staged setup to svpn-swap. It returns once the swap
	// has been launched; the swap itself stops this service.
	Apply func(Staged) error

	// Offer_control_planes receives the control plane list and released_at
	// of the newest manifest that verified; nil ignores them.
	Offer_control_planes func(list []string, released_at time.Time)

	Logger *slog.Logger
	Now    func() time.Time

	Check_interval   time.Duration // zero means six hours
	Retry_interval   time.Duration // after a failed check; zero means thirty minutes
	First_delay      time.Duration // zero means two minutes
	Apply_poll       time.Duration // how often a staged update looks for idle; zero means a minute
	Request_timeout  time.Duration // one manifest or signature fetch; zero means thirty seconds
	Download_timeout time.Duration // one setup download; zero means thirty minutes
	Stall_timeout    time.Duration // a download delivering nothing this long moves on; zero means a minute
	Swap_grace       time.Duration // how long a launched swap has to stop the service; zero means ten minutes
}

// Updater checks for, stages and applies new versions.
type Updater struct {
	config  Config
	current Version
	enabled bool
	logger  *slog.Logger
}

// Err_not_a_release is a build whose own version cannot be compared.
var Err_not_a_release = errors.New("update: this build carries no release version, so it does not update itself")

// A version that failed its swap is tried again after a while rather than
// never: the failure may have been the machine's -- an antivirus lock, a
// slow first start -- and not the build's.
const (
	failed_retry_after         = 24 * time.Hour
	failed_retry_below_minimum = time.Hour
)

// New builds an Updater. A build without a release version is not an
// error: the Updater is built disabled and says so once when run.
func New(config Config) (*Updater, error) {
	if config.Dir == "" || config.Apply == nil || config.Idle == nil {
		return nil, errors.New("update: Dir, Apply and Idle are required")
	}
	if config.Http_client == nil {
		// No overall Timeout: a thirty-megabyte setup over a bad link may
		// legitimately take long. Each phase that can stall forever is
		// bounded instead, and every request carries its own deadline.
		dialer := &net.Dialer{Timeout: 15 * time.Second}
		config.Http_client = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}}
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.DiscardHandler)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	defaults := []struct {
		field *time.Duration
		value time.Duration
	}{
		{&config.Check_interval, 6 * time.Hour},
		{&config.Retry_interval, 30 * time.Minute},
		{&config.First_delay, 2 * time.Minute},
		{&config.Apply_poll, time.Minute},
		{&config.Request_timeout, 30 * time.Second},
		{&config.Download_timeout, 30 * time.Minute},
		{&config.Stall_timeout, time.Minute},
		{&config.Swap_grace, 10 * time.Minute},
	}
	for _, d := range defaults {
		if *d.field == 0 {
			*d.field = d.value
		}
	}
	current, ok := Parse_version(config.Current_version)
	return &Updater{config: config, current: current, enabled: ok, logger: config.Logger}, nil
}

// channels lists where to look. The fallback is last: it is the one a
// censor knows about in advance, being in every copy of the binary.
// A mirror that is also the fallback is asked once.
func (u *Updater) channels(state State) []Channel {
	var list []Channel
	add := func(c Channel) {
		for _, have := range list {
			if have.Name == c.Name {
				return
			}
		}
		list = append(list, c)
	}
	if u.config.Control_planes != nil {
		for _, base := range u.config.Control_planes() {
			add(Control_plane_channel(base))
		}
	}
	for _, m := range state.Mirrors {
		if mirror_ok(m) == nil {
			add(channel_for(m))
		}
	}
	if u.config.Fallback != "" {
		add(channel_for(u.config.Fallback))
	}
	return list
}

// download_order is where the setup with sha256 is fetched from: first a
// channel whose bandwidth is free -- a code host's releases -- whose
// manifest named this very setup in the same check, since answering is
// the evidence it is reachable from here (the same version is not enough:
// a version signed again over a new build would send tens of megabytes
// down to be refused); then the channel that served the chosen manifest;
// then the rest. A code host that did not answer is still tried, among
// the rest: one failed fetch is not proof the next one fails too. The
// setup is checked against the manifest, so where it comes from does not
// matter for trust.
func (u *Updater) download_order(state State, served_by Channel, sha256 string, served map[string]string) []Channel {
	all := u.channels(state)
	var order []Channel
	add := func(c Channel) {
		for _, have := range order {
			if have.Name == c.Name {
				return
			}
		}
		order = append(order, c)
	}
	for _, c := range all {
		if c.prefer && served[c.Name] == sha256 {
			add(c)
		}
	}
	add(served_by)
	for _, c := range all {
		add(c)
	}
	return order
}

// Check looks for a newer release and stages it. It reports whether one
// is now staged.
//
// Every channel is asked and the newest verified manifest wins, rather
// than the first one that verifies: an old manifest is still validly
// signed, and whoever sits on the first channel could otherwise replay it
// to freeze this installation, or to steer it to a release with a known
// flaw. For the same reason a manifest older than the newest this
// installation has ever verified is ignored outright.
func (u *Updater) Check(ctx context.Context) (bool, error) {
	if !u.enabled {
		return false, Err_not_a_release
	}
	state, err := Load_state(u.config.Dir, u.logger)
	if err != nil {
		return false, fmt.Errorf("update: reading the update state: %w", err)
	}
	// The floor is never below this build: a manifest older than what is
	// running is a replay even on a fresh installation that has verified
	// nothing yet.
	highest, _ := Parse_version(state.Highest_version)
	if highest.Less(u.current) {
		highest = u.current
	}

	var best *Manifest
	var best_channel Channel
	var failures []error
	served := map[string]string{} // channel name -> the sha256 of the setup its manifest names
	for _, channel := range u.channels(state) {
		manifest, err := u.fetch_manifest(ctx, channel)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", channel.Name, err))
			continue
		}
		served[channel.Name] = manifest.Setup.Sha256
		v, _ := Parse_version(manifest.Version)
		if v.Less(highest) {
			failures = append(failures, fmt.Errorf("%s: serves %s, older than what this installation has seen", channel.Name, manifest.Version))
			continue
		}
		if best == nil {
			best, best_channel = &manifest, channel
			continue
		}
		// The same version from two channels: the later-released manifest
		// wins, so a stale copy cannot pin an old mirror list.
		if b, _ := Parse_version(best.Version); b.Less(v) || (b == v && best.Released_at.Before(manifest.Released_at)) {
			best, best_channel = &manifest, channel
		}
	}
	if best == nil {
		if len(failures) == 0 {
			return false, errors.New("update: no channel is configured")
		}
		return false, fmt.Errorf("update: no channel served a usable manifest: %w", errors.Join(failures...))
	}

	// The newest verified manifest refreshes the mirror list whether or
	// not it is newer than this build: that is how a replaced mirror
	// reaches installations that are already up to date.
	state.Highest_version = best.Version
	state.Mirrors = best.Mirrors
	if u.config.Offer_control_planes != nil {
		u.config.Offer_control_planes(best.Control_planes, best.Released_at)
	}
	staged, err := u.stage(ctx, &state, *best, u.download_order(state, best_channel, best.Setup.Sha256, served))
	if serr := Save_state(u.config.Dir, state); serr != nil && err == nil {
		err = serr
	}
	return staged, err
}

func (u *Updater) fetch_manifest(ctx context.Context, channel Channel) (Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, u.config.Request_timeout)
	defer cancel()
	raw, err := get(ctx, u.config.Http_client, channel.manifest, Max_manifest_bytes)
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	signature, err := get(ctx, u.config.Http_client, channel.signature, 1024)
	if err != nil {
		return Manifest{}, fmt.Errorf("signature: %w", err)
	}
	if err := Verify(raw, signature, u.config.Keys); err != nil {
		return Manifest{}, err
	}
	return Parse(raw)
}

// stage downloads the release if it is newer than this build, newer than
// or the same as anything already staged, and not a recent failure,
// trying channels in order.
func (u *Updater) stage(ctx context.Context, state *State, manifest Manifest, channels []Channel) (bool, error) {
	latest, _ := Parse_version(manifest.Version)
	if !u.current.Less(latest) {
		return false, nil
	}
	below_minimum := false
	if minimum, ok := Parse_version(manifest.Minimum_version); ok && u.current.Less(minimum) {
		below_minimum = true
	}
	if failed, ok := Parse_version(state.Failed_version); ok && failed == latest {
		wait := failed_retry_after
		if below_minimum {
			wait = failed_retry_below_minimum
		}
		if u.config.Now().Sub(state.Failed_at) < wait {
			return false, nil
		}
	}
	urgent := manifest.Urgent || below_minimum

	if s := state.Staged; s != nil {
		staged_version, _ := Parse_version(s.Version)
		if latest.Less(staged_version) {
			return true, nil // never replace a staged release with an older one
		}
		if staged_version == latest && s.Sha256 == manifest.Setup.Sha256 {
			if sum, err := File_sha256(s.Path); err == nil && sum == s.Sha256 {
				s.Urgent = urgent
				return true, nil
			}
		}
	}

	path := filepath.Join(u.config.Dir, manifest.Setup.File)
	var failures []error
	for _, channel := range channels {
		dctx, cancel := context.WithTimeout(ctx, u.config.Download_timeout)
		err := download(dctx, u.config.Http_client, channel.artifact(manifest.Version, manifest.Setup.File), manifest.Setup, path, u.config.Stall_timeout)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", channel.Name, err))
			continue
		}
		if old := state.Staged; old != nil && old.Path != path {
			os.Remove(old.Path)
		}
		state.Staged = &Staged{Version: manifest.Version, Path: path, Sha256: manifest.Setup.Sha256, Urgent: urgent}
		u.logger.Info("update staged", "version", manifest.Version, "urgent", urgent, "from", channel.Name)
		return true, nil
	}
	return false, fmt.Errorf("update: downloading %s: %w", manifest.Setup.File, errors.Join(failures...))
}

// Has_staged reports whether a staged release newer than this build is
// waiting -- what the service asks at start, before deciding whether to
// connect.
func (u *Updater) Has_staged() bool {
	if !u.enabled {
		return false
	}
	state, err := Load_state(u.config.Dir, u.logger)
	if err != nil || state.Staged == nil {
		return false
	}
	v, ok := Parse_version(state.Staged.Version)
	return ok && u.current.Less(v)
}

// Maybe_apply applies a staged update if it is urgent or nothing is
// carrying traffic, after checking the file is still the one verified. It
// reports whether the swap was launched.
func (u *Updater) Maybe_apply() (bool, error) {
	if !u.enabled {
		return false, nil
	}
	state, err := Load_state(u.config.Dir, u.logger)
	if err != nil {
		return false, fmt.Errorf("update: reading the update state: %w", err)
	}
	if state.Staged == nil {
		return false, nil
	}
	staged := *state.Staged
	if v, ok := Parse_version(staged.Version); !ok || !u.current.Less(v) {
		// This build is that version or newer: the swap that staged it has
		// finished, or something else installed past it.
		os.Remove(staged.Path)
		state.Staged = nil
		return false, Save_state(u.config.Dir, state)
	}
	if !staged.Launched_at.IsZero() && u.config.Now().Sub(staged.Launched_at) < Swap_window {
		return false, nil // a swap for it may still be running
	}
	if !staged.Urgent && !u.config.Idle() {
		return false, nil
	}
	if sum, err := File_sha256(staged.Path); err != nil || sum != staged.Sha256 {
		// Changed or gone since it was verified: forget it and let the
		// next check fetch it again.
		os.Remove(staged.Path)
		state.Staged = nil
		Save_state(u.config.Dir, state)
		return false, fmt.Errorf("update: the staged setup no longer matches what was verified")
	}
	// The launch is recorded before it happens: a swap launched without
	// the mark could be launched again over itself.
	state.Staged.Launched_at = u.config.Now()
	if err := Save_state(u.config.Dir, state); err != nil {
		return false, fmt.Errorf("update: recording the launch: %w", err)
	}
	u.logger.Info("applying update", "version", staged.Version, "urgent", staged.Urgent)
	if err := u.config.Apply(staged); err != nil {
		state.Staged.Launched_at = time.Time{}
		Save_state(u.config.Dir, state)
		return false, fmt.Errorf("update: launching the swap: %w", err)
	}
	return true, nil
}

// Run checks after First_delay and then every Check_interval (every
// Retry_interval after a failure, which once a tunnel is up also means
// "through the tunnel"), and applies a staged update as soon as it may.
// After launching a swap it waits Swap_grace for the swap to stop this
// service; if it is still running then, the swap failed early, and the
// loop carries on.
func (u *Updater) Run(ctx context.Context) {
	if !u.enabled {
		u.logger.Info("updates are off", "reason", Err_not_a_release.Error())
		return
	}
	next_check := u.config.Now().Add(u.config.First_delay)
	poll := time.NewTicker(u.config.Apply_poll)
	defer poll.Stop()
	for {
		launched, err := u.Maybe_apply()
		if err != nil {
			u.logger.Warn("update not applied", "err", err)
		}
		if launched {
			if !sleep(ctx, u.config.Swap_grace) {
				return
			}
			u.logger.Warn("the update swap did not stop the service; carrying on")
			continue
		}
		if !u.config.Now().Before(next_check) {
			if _, err := u.Check(ctx); err != nil {
				u.logger.Warn("update check failed", "err", err)
				next_check = u.config.Now().Add(u.config.Retry_interval)
			} else {
				next_check = u.config.Now().Add(u.config.Check_interval)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
