package vless

import (
	"context"
	"errors"
	"net/netip"
	"singularity-vpn-app/internal/protocols"
	"sync"
	"testing"
	"time"
)

type fake_process struct {
	done    chan struct{}
	ready   chan struct{}
	once    sync.Once
	cleaned chan struct{}
}

func (p *fake_process) Done() <-chan struct{}  { return p.done }
func (p *fake_process) Ready() <-chan struct{} { return p.ready }
func (p *fake_process) Stop() error            { p.once.Do(func() { close(p.done) }); return nil }
func Test_adapter_lifecycle(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit", true: "cancel"}[cancelled], func(t *testing.T) {
			p := &fake_process{done: make(chan struct{}), ready: make(chan struct{}), cleaned: make(chan struct{})}
			if !cancelled {
				close(p.ready)
			}
			a := new_adapter(func(context.Context, string) (netip.Addr, error) { return netip.MustParseAddr("203.0.113.9"), nil }, func([]byte) (process, func() error, error) {
				return p, func() error { close(p.cleaned); return nil }, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			err := a.Connect(ctx, protocols.Connection_config{Protocol: Name, Data: []byte(test_uri)})
			if cancelled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			} else {
				if err != nil || !a.Status().Running {
					t.Fatalf("connect: %v", err)
				}
				p.Stop()
			}
			select {
			case <-p.cleaned:
			case <-time.After(time.Second):
				t.Fatal("not cleaned")
			}
			if a.Status().Running {
				t.Fatal("exit still reports running")
			}
			if !cancelled && !a.Probe_required() {
				t.Fatal("unexpected exit downgraded the authenticated probe requirement")
			}
			if err := a.Disconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			if a.Probe_required() {
				t.Fatal("explicit disconnect did not retire the proxy probe requirement")
			}
		})
	}
}
