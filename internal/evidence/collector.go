package evidence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"singularity-vpn-app/internal/api"
	"sync"
	"time"
)

type Client interface {
	Capture_network_context(context.Context, string, *http.Client) (api.Network_context, error)
	Send_observations(context.Context, string, []api.Connection_observation) (api.Observation_ack, error)
}
type Capture func(context.Context) (string, *http.Client, error)
type Config struct {
	Store   *Store
	Client  Client
	Auth    func(context.Context) (string, error)
	Capture Capture
	Logger  *slog.Logger
	Now     func() time.Time
}
type snapshot struct {
	account, network string
	context          api.Network_context
}
type Collector struct {
	config           Config
	mu               sync.Mutex
	current          snapshot
	generation       uint64
	wake             chan struct{}
	dropped_reported uint64
}

func New(config Config) *Collector {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.DiscardHandler)
	}
	return &Collector{config: config, wake: make(chan struct{}, 1), current: snapshot{network: "unknown:" + New_id()}}
}
func (c *Collector) auth(ctx context.Context) (string, string, error) {
	token, err := c.config.Auth(ctx)
	if err != nil || token == "" {
		return "", "", fmt.Errorf("evidence: no active device identity")
	}
	return token, Account(token), nil
}

// Prepare runs before acquiring the connection lock and cannot prevent connecting.
func (c *Collector) Prepare(ctx context.Context) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	token, account, err := c.auth(ctx)
	if err != nil {
		c.Invalidate()
		return
	}
	c.mu.Lock()
	generation := c.generation
	c.mu.Unlock()
	material, direct, err := c.config.Capture(ctx)
	if direct != nil {
		defer direct.CloseIdleConnections()
	}
	if err != nil || direct == nil || material == "" {
		c.set_unknown(account, generation)
		return
	}
	network := c.config.Store.Fingerprint(material)
	c.mu.Lock()
	previous := c.current
	c.mu.Unlock()
	if previous.account == account && previous.network == network && previous.context.Context_id != "" && c.config.Now().Before(previous.context.Expires_at) {
		return
	}
	result, err := c.config.Client.Capture_network_context(ctx, token, direct)
	if err != nil {
		c.set_local_network(account, network, generation)
		if !api.Optional_unavailable(err) {
			c.config.Logger.Info("network context unavailable; observations remain local")
		}
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return
	}
	c.current = snapshot{account: account, network: network, context: result}
}
func (c *Collector) set_local_network(account, network string, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation == generation {
		c.current = snapshot{account: account, network: network}
	}
}
func (c *Collector) set_unknown(account string, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation == generation {
		c.current = snapshot{account: account, network: "unknown:" + New_id()}
	}
}
func (c *Collector) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.generation++
	c.current = snapshot{network: "unknown:" + New_id()}
	c.mu.Unlock()
}

// Network_changed compares physical fingerprints; tunnel notifications alone do not invalidate.
func (c *Collector) Network_changed(ctx context.Context) bool {
	if c == nil || ctx.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	material, direct, err := c.config.Capture(ctx)
	if direct != nil {
		direct.CloseIdleConnections()
	}
	c.mu.Lock()
	previous := c.current.network
	c.mu.Unlock()
	if err != nil || material == "" {
		c.Invalidate()
		return true
	}
	network := c.config.Store.Fingerprint(material)
	if previous == network {
		return false
	}
	c.Invalidate()
	_, account, auth_err := c.auth(ctx)
	if auth_err == nil {
		c.mu.Lock()
		c.current = snapshot{account: account, network: network}
		c.mu.Unlock()
	}
	return true
}
func (c *Collector) Context_id(ctx context.Context) string {
	if c == nil {
		return ""
	}
	_, account, err := c.auth(ctx)
	if err != nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current.account != account || !c.config.Now().Before(c.current.context.Expires_at) {
		return ""
	}
	return c.current.context.Context_id
}

// Record retains an event once locally; only scoped records enter the delivery queue.
func (c *Collector) Record(ctx context.Context, event api.Connection_observation) {
	if c == nil {
		return
	}
	_, account, err := c.auth(ctx)
	if err != nil {
		return
	}
	c.mu.Lock()
	snap := c.current
	c.mu.Unlock()
	if snap.account != account {
		snap = snapshot{account: account, network: "unknown:" + New_id()}
	}
	now := c.config.Now()
	event.Observed_at = now
	event.Observation_id = New_id()
	if event.Observation_id == "" {
		c.config.Logger.Warn("connection observation identifier unavailable")
		return
	}
	if !now.Before(snap.context.Captured_at) && now.Before(snap.context.Expires_at) {
		event.Context_id = snap.context.Context_id
	}
	if err := c.config.Store.Add(Record{Account: account, Network: snap.network, Context: snap.context, Observation: event}, now); err != nil {
		c.config.Logger.Warn("connection evidence persistence failed")
	}
	_, _, dropped := c.config.Store.Snapshot()
	c.mu.Lock()
	report_dropped := dropped > c.dropped_reported
	c.dropped_reported = dropped
	c.mu.Unlock()
	if report_dropped {
		c.config.Logger.Warn("connection evidence pending records evicted", "total", dropped)
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *Collector) Remember(ctx context.Context, rows []api.Connection_evidence) {
	if c == nil || len(rows) == 0 {
		return
	}
	_, account, err := c.auth(ctx)
	if err != nil {
		return
	}
	context_id := c.Context_id(ctx)
	if context_id == "" {
		return
	}
	summaries := []Summary{}
	c.mu.Lock()
	snap := c.current
	c.mu.Unlock()
	for _, row := range rows {
		if row.Validate() == nil && matches_context(row, snap.context) && c.config.Now().Sub(row.Observed_at) <= Summary_window {
			summaries = append(summaries, Summary{Account: account, Context_id: context_id, Evidence: row})
		}
	}
	if err := c.config.Store.Acknowledge(account, nil, summaries, c.config.Now()); err != nil {
		c.config.Logger.Warn("connection evidence summaries could not be saved")
	}
}

// Flush never holds store or collector locks during HTTP. Only this identity's records leave.
func (c *Collector) Flush(ctx context.Context) error {
	token, account, err := c.auth(ctx)
	if err != nil {
		return err
	}
	batch := c.config.Store.Pending(account, c.config.Now())
	if len(batch) == 0 {
		return nil
	}
	reply, err := c.config.Client.Send_observations(ctx, token, batch)
	if err != nil {
		var remote *api.Error
		if errors.As(err, &remote) && (remote.Status == http.StatusBadRequest || remote.Status == http.StatusForbidden || remote.Status == http.StatusConflict || remote.Status == http.StatusUnprocessableEntity) {
			ids := []string{}
			for _, event := range batch {
				ids = append(ids, event.Observation_id)
			}
			if save_err := c.config.Store.Reject(account, ids, fmt.Sprintf("http_%d", remote.Status), c.config.Now()); save_err != nil {
				return save_err
			}
			c.config.Logger.Warn("connection evidence delivery rejected; records remain local", "status", remote.Status, "records", len(ids))
			return nil
		}
		return err
	}
	// Each batch has one original context, including delayed delivery after expiry.
	summaries := []Summary{}
	for _, row := range reply.Evidence {
		if row.Validate() != nil || !matches_context(row, c.config.Store.Context(account, batch[0].Context_id)) {
			continue
		}
		summaries = append(summaries, Summary{Account: account, Context_id: batch[0].Context_id, Evidence: row})
	}
	return c.config.Store.Acknowledge(account, reply.Accepted_ids, summaries, c.config.Now())
}
func (c *Collector) Run(ctx context.Context) {
	delay := time.Second
	allow_wake := true
	for {
		timer := time.NewTimer(delay)
		var wake <-chan struct{}
		if allow_wake {
			wake = c.wake
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-wake:
			timer.Stop()
		}
		request_ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := c.Flush(request_ctx)
		cancel()
		allow_wake = err == nil
		if err == nil {
			delay = time.Minute
		} else if api.Optional_unavailable(err) {
			delay = 5 * time.Minute
		} else {
			delay *= 2
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
		}
	}
}

func matches_context(row api.Connection_evidence, context api.Network_context) bool {
	if context.Context_id == "" || row.Scope != context.Scope || row.Country != context.Country || row.Asn != context.Asn || row.Region != context.Region {
		return false
	}
	return true
}

// Local_summaries reports this installation's own observations independently of CP echoes.
func (c *Collector) Local_summaries(ctx context.Context) []Local_summary {
	if c == nil {
		return nil
	}
	_, account, err := c.auth(ctx)
	if err != nil {
		return nil
	}
	c.mu.Lock()
	network := c.current.network
	c.mu.Unlock()
	return c.config.Store.Local_summaries(account, network, c.config.Now())
}
