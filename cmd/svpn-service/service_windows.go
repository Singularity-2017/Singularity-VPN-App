//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/buildinfo"
	"singularity-vpn-app/internal/core"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/platform"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/protocols/wireguard"
	"singularity-vpn-app/internal/reach"
	"singularity-vpn-app/internal/security"
	"singularity-vpn-app/internal/state"
)

const (
	service_name = "SingularityVPN"
	adapter_name = "Singularity"
)

// The prober's targets. Both are outside the control plane on purpose: a
// control plane that is down for maintenance says nothing about whether a
// person's tunnel carries traffic, and measuring the tunnel against it
// would move people off healthy nodes whenever the control plane
// restarted. Both are anycast, so through a full tunnel they are reached
// from the node's own network, near it.
//
// They are a guess, not a measurement: nobody has yet checked which
// public target answers most reliably from the nodes this service connects
// to. A node whose network blocks one of them would look lost to every
// probe. The log records every verdict, which is how that would show.
const (
	probe_dial_target = "1.1.1.1:443"
	probe_dns_name    = "cloudflare.com"
)

// serve_drain bounds how long a stop waits for the pipe's in-flight
// requests to finish before tearing the tunnel down anyway. A connect in
// flight sees its context cancelled at the same moment and returns well
// inside this.
const serve_drain = 10 * time.Second

// The two resume notifications, from winuser.h; x/sys does not carry them.
// Automatic arrives on every resume, Suspend only when a person is at the
// machine -- either one means the tunnel may have died while it slept.
const (
	pbt_apmresumesuspend   = 0x0007
	pbt_apmresumeautomatic = 0x0012
)

// Exit codes the service reports to the service control manager as
// service-specific errors, so that "failed to start" and "failed to stop
// cleanly" are told apart in `sc query` without opening the log.
const (
	exit_start_failed = 1
	exit_stop_failed  = 2
	exit_pipe_failed  = 3
)

// handler is the service's svc.Handler, the same under the service control
// manager and under `svpn-service run`.
type handler struct {
	elog    debug.Log
	console bool // also write the log to stderr, for `run`
}

func (h *handler) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	// Reported before anything slow: the service control manager is timing
	// the start from here.
	status <- svc.Status{State: svc.StartPending, WaitHint: 30_000}

	running, err := start(h.console)
	if err != nil {
		// The event log as well as the file: in the failures that matter
		// most -- the directory, the log itself -- there is no file.
		h.elog.Error(1, "Singularity VPN did not start: "+err.Error())
		return true, exit_start_failed
	}
	h.elog.Info(1, "Singularity VPN started.")

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPowerEvent}

	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// The wait hint covers the drain and core's own bounded
				// teardown, so the service control manager does not give
				// up on a stop that is still taking routes off the machine.
				status <- svc.Status{State: svc.StopPending, WaitHint: 45_000}
				if err := running.stop(); err != nil {
					h.elog.Error(2, "Singularity VPN stopped with an error: "+err.Error())
					return true, exit_stop_failed
				}
				return false, 0
			case svc.PowerEvent:
				if request.EventType == pbt_apmresumeautomatic || request.EventType == pbt_apmresumesuspend {
					// In a goroutine: this loop must answer the service
					// control manager promptly, and a recheck runs a
					// network probe that can take seconds.
					running.logger.Info("resumed from sleep; checking the connection")
					go running.application.Recheck(running.ctx)
				}
			default:
				// Nothing else was asked for in Accepts; ignore it and
				// leave the reported status as it is.
			}
		case err := <-running.served:
			// Serve returns on its own only when the pipe itself broke. A
			// service without its pipe cannot be told to disconnect, so it
			// must not sit there holding a tunnel: take everything down
			// and let the recovery actions start it again.
			running.logger.Error("the pipe stopped serving", "error", err.Error())
			running.served = nil
			stop_err := running.stop()
			message := "Singularity VPN lost its pipe and stopped: " + err.Error()
			if stop_err != nil {
				message += "; stopping also failed: " + stop_err.Error()
			}
			h.elog.Error(3, message)
			return true, exit_pipe_failed
		}
	}
}

// running_service is everything start built and stop must take apart.
type running_service struct {
	application *core.Application
	ctx         context.Context
	cancel      context.CancelFunc
	served      chan error
	logger      *slog.Logger
	log_file    io.Closer
}

// update_settle is how long a starting service waits before launching a
// staged update's swap -- long enough to have reported Running. update_grace
// is how long it then waits to be stopped by the swap before carrying on as
// if no update had been staged.
const (
	update_settle = 10 * time.Second
	update_grace  = 3 * time.Minute
)

func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// start builds the service in the order that leaves the least to unwind.
// The pipe is created before the application starts: if the pipe cannot be
// created -- including because something else has taken its name -- then
// nothing that touches the network has started, and there is no tunnel
// manager running behind a pipe somebody else owns.
func start(console bool) (*running_service, error) {
	p, err := service_paths()
	if err != nil {
		return nil, err
	}
	if err := ensure_directories(p); err != nil {
		return nil, err
	}

	log_file, err := open_capped_writer(p.log, max_log_bytes)
	if err != nil {
		return nil, err
	}
	var log_out io.Writer = log_file
	if console {
		log_out = io.MultiWriter(log_file, os.Stderr)
	}
	logger := slog.New(slog.NewTextHandler(log_out, nil))
	logger.Info("service starting", build_attributes()...)

	application, err := build_application(p, logger)
	if err != nil {
		logger.Error("service did not start", "error", err.Error())
		log_file.Close()
		return nil, err
	}

	listener, err := ipc.Listen()
	if err != nil {
		logger.Error("service did not start", "error", err.Error())
		log_file.Close()
		return nil, err
	}

	// A staged update is applied at start, before anything connects: a
	// start is the one moment nothing can be carrying traffic. It is
	// launched a little later, from a goroutine, because the swap's setup
	// stops this service, and a service still reporting START_PENDING does
	// not accept a stop. The connection is held back meanwhile, and
	// started only if the swap never stops us.
	updater := build_updater(p, application, logger)
	pending := updater != nil && updater.Has_staged()

	ctx, cancel := context.WithCancel(context.Background())
	if pending {
		go func() {
			start_normally := func() {
				if ctx.Err() != nil {
					return // stopped meanwhile; Stop has already run
				}
				if err := application.Start(ctx); err != nil {
					logger.Error("starting after an update that did not happen", "error", err.Error())
				}
				go updater.Run(ctx)
			}
			if !wait(ctx, update_settle) {
				return
			}
			launched, err := updater.Maybe_apply()
			if err != nil {
				logger.Warn("staged update not applied", "err", err)
			}
			if !launched {
				start_normally()
				return
			}
			if !wait(ctx, update_grace) {
				return
			}
			logger.Warn("the update swap did not stop the service; starting normally")
			start_normally()
		}()
	} else {
		if err := application.Start(ctx); err != nil {
			cancel()
			listener.Close()
			logger.Error("service did not start", "error", err.Error())
			log_file.Close()
			return nil, err
		}
		if updater != nil {
			go updater.Run(ctx)
		}
	}

	served := make(chan error, 1)
	go func() { served <- serve(ctx, listener, application) }()

	logger.Info("service running", "pipe", ipc.Pipe_name)
	return &running_service{
		application: application,
		ctx:         ctx,
		cancel:      cancel,
		served:      served,
		logger:      logger,
		log_file:    log_file,
	}, nil
}

// serve is ipc.Serve with its expected ending taken out: a cancelled
// context is how a stop ends it, and is not a failure.
func serve(ctx context.Context, listener net.Listener, application *core.Application) error {
	err := ipc.Serve(ctx, listener, application)
	if ctx.Err() != nil {
		return nil
	}
	if err == nil {
		err = errors.New("ipc: Serve returned without an error")
	}
	return err
}

func build_application(p paths, logger *slog.Logger) (*core.Application, error) {
	secrets, err := security.Open_file_store(p.secrets, security.New_dpapi_cipher())
	if err != nil {
		return nil, err
	}
	store, err := state.Open(p.state)
	if err != nil {
		return nil, err
	}
	control_plane, err := api.New(api.Config{Base_url: buildinfo.Control_plane})
	if err != nil {
		return nil, err
	}
	adapter, err := wireguard.New_windows_adapter(adapter_name)
	if err != nil {
		return nil, err
	}
	registry, err := protocols.New_registry(adapter)
	if err != nil {
		return nil, err
	}
	prober, err := platform.New_network_prober(platform.Prober_config{
		Tunnel_up:   func() bool { return adapter.Status().Running },
		Dns_name:    probe_dns_name,
		Dial_target: probe_dial_target,
	})
	if err != nil {
		return nil, err
	}

	return core.New_application(core.Dependencies{
		Control_plane: control_plane,
		Secrets:       secrets,
		Store:         store,
		Protocols:     registry,
		Prober:        prober,
		Reach_prober:  reach.New_prober(reach.System_network{}),
		Bus:           events.New_bus(0),
		Device_name:   device_name(),
		Logger:        logger,
	})
}

// device_name is what the device token is called on the account page, so
// the person can tell which machine to revoke. The computer's name is the
// one thing they will recognise.
func device_name() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "Windows computer"
	}
	return host + " (Windows)"
}

// build_attributes names only what the build supplied -- never the control
// plane address, which does not belong in a log a person may send somebody
// to diagnose a problem.
func build_attributes() []any {
	var attributes []any
	if buildinfo.Version != "" {
		attributes = append(attributes, "version", buildinfo.Version)
	}
	if buildinfo.Commit != "" {
		attributes = append(attributes, "commit", buildinfo.Commit)
	}
	if buildinfo.Built_at != "" {
		attributes = append(attributes, "built", buildinfo.Built_at)
	}
	return attributes
}

// stop takes the service down in the one order that cannot strand a
// tunnel: stop accepting and cancel what is in flight, give the pipe a
// bounded time to drain, then tear the tunnel down. core.Application.Stop
// uses a context of its own for that teardown, so the cancellation that
// began the stop cannot reach the calls that take routes and DNS back off
// the machine.
func (r *running_service) stop() error {
	r.logger.Info("service stopping")
	r.cancel()

	if r.served != nil {
		select {
		case err := <-r.served:
			if err != nil {
				r.logger.Warn("the pipe ended with an error", "error", err.Error())
			}
		case <-time.After(serve_drain):
			r.logger.Warn("the pipe did not drain in time; tearing down anyway")
		}
	}

	err := r.application.Stop(context.Background())
	if err != nil {
		r.logger.Error("service stopped with an error", "error", err.Error())
	} else {
		r.logger.Info("service stopped")
	}
	r.log_file.Close()
	if err != nil {
		return fmt.Errorf("stopping: %w", err)
	}
	return nil
}
