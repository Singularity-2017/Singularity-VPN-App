package vless

import (
	"context"
	"errors"
	"net/netip"
	"singularity-vpn-app/internal/protocols"
	"sync"
	"time"
)

type process interface {
	Done() <-chan struct{}
	Ready() <-chan struct{}
	Stop() error
}
type starter func([]byte) (process, func() error, error)
type resolver func(context.Context, string) (netip.Addr, error)
type session struct {
	proc    process
	cleanup func() error
	since   time.Time
	once    sync.Once
	err     error
}

func (s *session) release() error {
	s.once.Do(func() { s.err = errors.Join(s.proc.Stop(), s.cleanup()) })
	return s.err
}

type Adapter struct {
	op             sync.Mutex
	mu             sync.Mutex
	current        *session
	probe_required bool
	start          starter
	resolve        resolver
}

func new_adapter(resolve resolver, start starter) *Adapter {
	return &Adapter{start: start, resolve: resolve}
}

// Probe_required remains true after an unexpected exit until Disconnect retires
// that session. A dead proxy must not downgrade its health check to direct TCP.
func (a *Adapter) Probe_required() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.probe_required
}
func (*Adapter) Name() string            { return Name }
func (*Adapter) Whole_machine_only()     {}
func (*Adapter) Full_probe_routes() bool { return true }
func (*Adapter) Probe_target() string    { return "" }
func (a *Adapter) Status() protocols.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.current
	if s == nil {
		return protocols.Status{}
	}
	select {
	case <-s.proc.Done():
		return protocols.Status{}
	default:
		return protocols.Status{Running: true, Since: s.since, Detail: "Xray native full TUN"}
	}
}
func (a *Adapter) Connect(ctx context.Context, c protocols.Connection_config) error {
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	old := a.current
	a.mu.Unlock()
	if old != nil {
		return errors.New("vless: already connected")
	}
	if c.Protocol != Name {
		return Err_config
	}
	u, err := Parse_uri(c.Data)
	if err != nil {
		return err
	}
	endpoint, err := a.resolve(ctx, u.host)
	if err != nil {
		return errors.New("vless: endpoint resolution failed")
	}
	if !endpoint.IsValid() || endpoint.IsLoopback() || endpoint.IsUnspecified() || endpoint.IsMulticast() {
		return Err_config
	}
	data, err := u.native_config(endpoint.Unmap(), adapter_name)
	if err != nil {
		return Err_config
	}
	p, cleanup, err := a.start(data)
	clear(data)
	if err != nil {
		return err
	}
	s := &session{proc: p, cleanup: cleanup}
	fail := func(err error) error { return errors.Join(err, s.release()) }
	select {
	case <-ctx.Done():
		return fail(ctx.Err())
	case <-p.Done():
		return fail(errors.New("vless: Xray exited before TUN readiness"))
	case <-p.Ready():
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	s.since = time.Now()
	a.mu.Lock()
	a.current = s
	a.probe_required = true
	a.mu.Unlock()
	go func() {
		<-p.Done()
		a.op.Lock()
		defer a.op.Unlock()
		s.release()
		a.mu.Lock()
		if a.current == s {
			a.current = nil
		}
		a.mu.Unlock()
	}()
	return nil
}
func (a *Adapter) Disconnect(context.Context) error {
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	s := a.current
	a.probe_required = false
	a.mu.Unlock()
	if s == nil {
		return nil
	}
	err := s.release()
	a.mu.Lock()
	if a.current == s {
		a.current = nil
	}
	a.mu.Unlock()
	return err
}

const adapter_name = "Singularity VLESS"
