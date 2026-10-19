//go:build windows

package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"singularity-vpn-app/internal/ipc"
)

// call_timeout bounds one call to the service. A connect can legitimately
// take most of the service's own connect timeout, so this is above it.
const call_timeout = 60 * time.Second

// redial_interval is how often a missing service is looked for again.
const redial_interval = 2 * time.Second

// App is what the page calls. Every exported method is reachable from
// JavaScript as window.go.main.App.<Method>; that is the whole surface the
// page has, and it is the pipe's command set seen from the other side --
// nothing here reaches the network or the system by itself.
type App struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	client *ipc.Client

	tray *tray
}

func new_app() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.tray = start_tray(a)
	go a.watch()
}

func (a *App) shutdown() {
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Lock()
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	a.mu.Unlock()
}

func (a *App) show() {
	if a.ctx == nil {
		return
	}
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
}

func (a *App) quit() {
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
}

// Status_view is what the page renders. Service_running false means the
// pipe is not there; Status is then absent, not a zero value that would
// read as "disconnected".
type Status_view struct {
	Service_running bool               `json:"service_running"`
	Status          *ipc.Status_result `json:"status,omitempty"`
	Error           *Outcome           `json:"error,omitempty"`
}

// Outcome is the answer to an action. An empty Code means it was done; a
// code from ipc (not_signed_in, unauthorized, busy...) or one of this
// file's own says why not, and Message is the service's wording.
type Outcome struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

const (
	code_service_not_running = "service_not_running"
	code_untrusted_pipe      = "untrusted_pipe"
	code_failed              = "failed"
)

func (a *App) Status() Status_view {
	client, outcome := a.connected_client()
	if outcome != nil {
		if outcome.Code == code_service_not_running {
			return Status_view{}
		}
		return Status_view{Error: outcome}
	}
	ctx, cancel := context.WithTimeout(a.ctx, call_timeout)
	defer cancel()
	status, err := client.Status(ctx)
	if err != nil {
		return Status_view{Service_running: !a.dropped(client, err), Error: outcome_of(err)}
	}
	a.tray.show_status(&status)
	return Status_view{Service_running: true, Status: &status}
}

func (a *App) Connect() Outcome {
	return a.act(func(ctx context.Context, c *ipc.Client) error {
		return c.Connect(ctx, ipc.Connect_params{Mode: ipc.Mode_auto})
	})
}

func (a *App) Disconnect() Outcome {
	return a.act(func(ctx context.Context, c *ipc.Client) error { return c.Disconnect(ctx) })
}

// Sign_in hands the username and password to the service in one call and
// keeps neither: the service exchanges them for a device token it stores
// itself, and this process never sees that token (design section 2).
func (a *App) Sign_in(username, password string) Outcome {
	return a.act(func(ctx context.Context, c *ipc.Client) error {
		_, err := c.Sign_in(ctx, ipc.Sign_in_params{Username: username, Password: password})
		return err
	})
}

func (a *App) Sign_out() Outcome {
	return a.act(func(ctx context.Context, c *ipc.Client) error { return c.Sign_out(ctx) })
}

// Strategy_view is the stored strategy, or why it could not be read.
type Strategy_view struct {
	Strategy *ipc.Strategy `json:"strategy,omitempty"`
	Error    *Outcome      `json:"error,omitempty"`
}

func (a *App) Strategy() Strategy_view {
	var view Strategy_view
	outcome := a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		s, err := c.Strategy(ctx)
		if err == nil {
			view.Strategy = &s
		}
		return err
	})
	if outcome.Code != "" {
		view.Error = &outcome
	}
	return view
}

// Set_strategy records what the connection is for. If a connection is up,
// the service moves it, which is why this can take as long as a connect.
func (a *App) Set_strategy(strategy ipc.Strategy) Outcome {
	return a.act(func(ctx context.Context, c *ipc.Client) error { return c.Set_strategy(ctx, strategy) })
}

// Servers_view is the server list, or why it could not be had.
type Servers_view struct {
	Servers []ipc.Server `json:"servers"`
	Error   *Outcome     `json:"error,omitempty"`
}

func (a *App) Servers() Servers_view {
	view := Servers_view{Servers: []ipc.Server{}}
	outcome := a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		result, err := c.Servers(ctx)
		if err == nil && result.Servers != nil {
			view.Servers = result.Servers
		}
		return err
	})
	if outcome.Code != "" {
		view.Error = &outcome
	}
	return view
}

// Version is the service's version. Fields the build did not supply are
// absent, and the page prints nothing for them.
func (a *App) Version() ipc.Version_result {
	client, outcome := a.connected_client()
	if outcome != nil {
		return ipc.Version_result{}
	}
	ctx, cancel := context.WithTimeout(a.ctx, call_timeout)
	defer cancel()
	version, err := client.Version(ctx)
	if err != nil {
		a.dropped(client, err)
		return ipc.Version_result{}
	}
	return version
}

func (a *App) act(do func(context.Context, *ipc.Client) error) Outcome {
	client, outcome := a.connected_client()
	if outcome != nil {
		return *outcome
	}
	ctx, cancel := context.WithTimeout(a.ctx, call_timeout)
	defer cancel()
	if err := do(ctx, client); err != nil {
		a.dropped(client, err)
		return *outcome_of(err)
	}
	a.changed()
	return Outcome{}
}

// act_quietly is act for a read: nothing changed, so nothing is announced.
func (a *App) act_quietly(do func(context.Context, *ipc.Client) error) Outcome {
	client, outcome := a.connected_client()
	if outcome != nil {
		return *outcome
	}
	ctx, cancel := context.WithTimeout(a.ctx, call_timeout)
	defer cancel()
	if err := do(ctx, client); err != nil {
		a.dropped(client, err)
		return *outcome_of(err)
	}
	return Outcome{}
}

// connected_client returns the live client, dialling the pipe if there is
// none. Dial verifies the pipe's owner before anything is sent, which is
// what stands between a password and a program squatting on the name.
func (a *App) connected_client() (*ipc.Client, *Outcome) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		return a.client, nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, redial_interval)
	defer cancel()
	conn, err := ipc.Dial(ctx)
	if err != nil {
		return nil, outcome_of(err)
	}
	a.client = ipc.New_client(conn)
	return a.client, nil
}

// dropped forgets client when err says the connection itself is gone, so
// the next call dials again, and reports whether it did. A refusal from the
// service is an answer on a working connection and keeps it.
func (a *App) dropped(client *ipc.Client, err error) bool {
	var remote *ipc.Remote_error
	if errors.As(err, &remote) {
		return false
	}
	a.mu.Lock()
	if a.client == client {
		a.client.Close()
		a.client = nil
	}
	a.mu.Unlock()
	return true
}

func outcome_of(err error) *Outcome {
	var remote *ipc.Remote_error
	switch {
	case errors.As(err, &remote):
		return &Outcome{Code: remote.Code, Message: remote.Message}
	case errors.Is(err, ipc.Err_service_not_running):
		return &Outcome{Code: code_service_not_running, Message: err.Error()}
	case errors.Is(err, ipc.Err_untrusted_pipe):
		return &Outcome{Code: code_untrusted_pipe, Message: err.Error()}
	default:
		return &Outcome{Code: code_failed, Message: err.Error()}
	}
}

// changed tells the page to fetch the status again now rather than at its
// next poll.
func (a *App) changed() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "changed")
	}
}

// watch keeps a subscription open whenever the service is there, so the
// page and the tray hear about a state change the moment it happens -- a
// switch recovery made, a tunnel that came up after a reboot -- instead of
// at the next poll. Without the service it looks again every
// redial_interval.
func (a *App) watch() {
	for a.ctx.Err() == nil {
		client, outcome := a.connected_client()
		if outcome != nil {
			a.tray.show_status(nil)
			a.changed()
			if !sleep(a.ctx, redial_interval) {
				return
			}
			continue
		}
		events, err := client.Subscribe(a.ctx)
		if err != nil {
			a.dropped(client, err)
			continue
		}
		a.Status()
		a.changed()
		for range events {
			a.Status()
			a.changed()
		}
		// The channel closes when the connection does.
		a.dropped(client, errors.New("subscription ended"))
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
