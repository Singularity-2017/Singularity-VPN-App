//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/buildinfo"
	"singularity-vpn-app/internal/control_address"
	"singularity-vpn-app/internal/core"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/evidence"
	"singularity-vpn-app/internal/ipc"
	"singularity-vpn-app/internal/journal"
	"singularity-vpn-app/internal/matchserver"
	"singularity-vpn-app/internal/platform"
	winplatform "singularity-vpn-app/internal/platform/windows"
	"singularity-vpn-app/internal/platform/windows/divert"
	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/protocols/openvpn"
	"singularity-vpn-app/internal/protocols/wireguard"
	"singularity-vpn-app/internal/reach"
	"singularity-vpn-app/internal/security"
	"singularity-vpn-app/internal/state"
	"singularity-vpn-app/internal/update"
)

const (
	service_name = "SingularityVPN"
	adapter_name = "Singularity"
	// openvpn_adapter_name is the wintun adapter openvpn.exe runs on. It is
	// not the WireGuard one: each protocol owns its adapter, and only one of
	// them exists while a tunnel is up.
	openvpn_adapter_name = "Singularity OpenVPN"
)

// network_settle is how long after the last notification of a burst a
// network change is acted on: one real change (a Wi-Fi roam, a cable
// plugged in) produces dozens of route and interface notifications.
const network_settle = 3 * time.Second

// tunnel_luids caches which LUIDs belong to this service's own tunnel
// adapters. Caching avoids 80+ GetIfEntry2 (\Device\Nsi) kernel calls when
// AntiZapret routes are added on the tunnel, and still recognises the tunnel's
// LUID when its delete notifications arrive after the adapter is already gone.
var tunnel_luids sync.Map

// is_tunnel_adapter reports whether the interface is one this service
// creates. Notifications about it are the service's own routes and addresses
// being set or taken off, not a change of the network under it.
func is_tunnel_adapter(luid winipcfg.LUID) bool {
	if cached, ok := tunnel_luids.Load(luid); ok {
		return cached.(bool)
	}
	row, err := luid.Interface()
	if err != nil {
		return false
	}
	alias := row.Alias()
	ours := alias == adapter_name || alias == openvpn_adapter_name
	tunnel_luids.Store(luid, ours)
	return ours
}

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
	operations  *journal.Journal

	// network_changes debounces the watcher; stop_watching ends it. Both
	// are nil when the watcher could not be started.
	network_changes *debouncer
	stop_watching   func() error
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
	// The journal outlives the service, so the window shows when it last
	// stopped -- or that it never got to.
	operations := journal.Open(filepath.Join(p.root, "journal.json"), journal_size)
	logger := slog.New(journal.Handler(slog.NewTextHandler(log_out, nil), operations))
	logger.Info("service starting", build_attributes()...)
	// A service that died connected left its DNS rule behind, pointing
	// every name at a tunnel that is gone.
	if err := winplatform.Remove_dns_policy(); err != nil {
		logger.Warn("removing a DNS rule left from before failed", "error", err.Error())
	}

	// The control plane's current address: compiled in at first, replaced
	// by the list a signed manifest names; see docs/adr/0001.
	addresses := control_address.Open(p.root, buildinfo.Control_plane, logger)
	addresses.On_change(func([]string) {
		logger.Info(control_plane_moved)
	})

	// The updater is built after the application but hears whether the
	// control plane answers: a long silence brings its next check forward.
	var silence_listener atomic.Pointer[update.Updater]
	answered := func(ok bool) {
		if u := silence_listener.Load(); u != nil {
			u.Control_plane_answered(ok)
		}
	}

	application, err := build_application(p, logger, operations, addresses, answered)
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
	updater := build_updater(p, application, addresses, logger)
	if updater != nil {
		silence_listener.Store(updater)
	}
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

	// A change on a physical adapter is a network change. One on a virtual
	// adapter -- another VPN, Hyper-V or WSL restarting and rebuilding its
	// routes -- counts only when it moved the physical path.
	var path_mu sync.Mutex
	var physical_touched atomic.Bool
	// The one full read of the interface table: from here on every
	// notification is classed from what it found.
	if err := winplatform.Warm_hardware_interfaces(); err != nil {
		logger.Warn("adapters not classified up front; each is read when it first changes", "error", err.Error())
	}
	path_now := winplatform.Physical_path_key()
	network_changes := new_debouncer(network_settle, real_scheduler, func() {
		touched := physical_touched.Swap(false)
		path_mu.Lock()
		before := path_now
		path_mu.Unlock()
		if !path_may_have_moved(touched, before) {
			return
		}
		path := winplatform.Physical_path_key()
		path_mu.Lock()
		path_now = path
		path_mu.Unlock()
		if !network_moved(touched, before, path) {
			return
		}
		logger.Info("network changed; repinning or re-analysing")
		go application.Network_changed(ctx)
	})
	stop_watching, err := winplatform.Watch_network(func(luid winipcfg.LUID) bool {
		if is_tunnel_adapter(luid) {
			return true
		}
		if winplatform.Is_hardware_interface(luid) {
			physical_touched.Store(true)
		}
		return false
	}, network_changes.Trigger)
	if err != nil {
		// Not fatal: the tunnel works without it, it just does not follow
		// a network change on its own.
		logger.Warn("not watching network changes", "error", err.Error())
		network_changes.Stop()
		network_changes, stop_watching = nil, nil
	}

	logger.Info("service running", "pipe", ipc.Pipe_name)
	return &running_service{
		network_changes: network_changes,
		stop_watching:   stop_watching,
		application:     application,
		ctx:             ctx,
		cancel:          cancel,
		served:          served,
		logger:          logger,
		log_file:        log_file,
		operations:      operations,
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

// enable_games gives the adapter the WinDivert game router. The driver's
// files sit beside this executable; the marker that records an installed
// driver as ours lives in the protected data directory, so nobody but an
// administrator can make the service delete another program's driver. In
// games-only mode the tunnel carries the probe target too, or the probe
// would measure the direct path.
func enable_games(adapter *wireguard.Adapter, p paths, logger *slog.Logger) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	probe, err := netip.ParseAddrPort(probe_dial_target)
	if err != nil {
		return fmt.Errorf("the probe target %q is not an address: %w", probe_dial_target, err)
	}
	driver := divert.New_driver(filepath.Join(p.root, "windivert.ours"), filepath.Dir(exe))
	router := divert.New_router(filepath.Dir(exe), driver, logger)
	adapter.Enable_games(router, []netip.Prefix{netip.PrefixFrom(probe.Addr(), probe.Addr().BitLen())})
	return nil
}

// openvpn_adapter is the OpenVPN protocol over the openvpn.exe installed
// beside this executable, in openvpn\. Without it -- a build made before
// OpenVPN was bundled -- the protocol is simply not offered, and the state
// machine moves to an alternative this build has, which is what a missing
// protocol has always meant here.
func openvpn_adapter(p paths, logger *slog.Logger) *openvpn.Adapter {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	path := filepath.Join(filepath.Dir(exe), "openvpn", "openvpn.exe")
	if _, err := os.Stat(path); err != nil {
		logger.Info("openvpn.exe is not installed; OpenVPN is not offered", "path", path)
		return nil
	}
	return openvpn.New_windows_adapter(path, openvpn_adapter_name, filepath.Join(p.root, "openvpn.ovpn"), logger)
}

// journal_size is how many operations the window can read back: a few
// sessions' worth of connects, switches and failures.
const journal_size = 300

// control_plane_moved is the journal line for a new control plane address.
// It carries no address: the journal is shown and sent in problem reports.
const control_plane_moved = "the control plane moved; its address came from a signed manifest"

func build_application(p paths, logger *slog.Logger, operations *journal.Journal, addresses *control_address.Store, answered func(ok bool)) (*core.Application, error) {
	secrets, err := security.Open_file_store(p.secrets, security.New_dpapi_cipher())
	if err != nil {
		return nil, err
	}
	store, err := state.Open(p.state)
	if err != nil {
		return nil, err
	}
	control_plane, err := api.New(api.Config{Base_urls: addresses.Current, Answered: answered})
	if err != nil {
		return nil, err
	}
	adapter, err := wireguard.New_windows_adapter(adapter_name)
	if err != nil {
		return nil, err
	}
	if err := enable_games(adapter, p, logger); err != nil {
		return nil, err
	}
	adapters := []protocols.Protocol{adapter, wireguard.Amnezia{Adapter: adapter}}
	tunnel_up := func() bool { return adapter.Status().Running }
	probe_target := adapter.Probe_target
	fallback_allowed := adapter.Full_probe_routes
	if ovpn := openvpn_adapter(p, logger); ovpn != nil {
		adapters = append(adapters, ovpn)
		// Corroboration follows the main tunnel, including the shared Amnezia adapter.
		fallback_allowed = func() bool {
			if adapter.Status().Running {
				return adapter.Full_probe_routes()
			}
			return ovpn.Status().Running && ovpn.Full_probe_routes()
		}
		tunnel_up = func() bool { return adapter.Status().Running || ovpn.Status().Running }
		// The WireGuard tunnel, when it is up, is the one the connection
		// is: OpenVPN may run beside it only to carry AntiZapret next to
		// games, and the probe measures the connection, not that one.
		probe_target = func() string {
			if adapter.Status().Running {
				return adapter.Probe_target()
			}
			return ovpn.Probe_target()
		}
	}
	registry, err := protocols.New_registry(adapters...)
	if err != nil {
		return nil, err
	}
	prober, err := platform.New_network_prober(platform.Prober_config{
		Tunnel_up:   tunnel_up,
		Dns_name:    probe_dns_name,
		Dial_target: probe_dial_target,
		// A tunnel that carries only part of the traffic drops the
		// static target; the adapter names one it does carry.
		Dial_target_now:  probe_target,
		Fallback_target:  "8.8.8.8:443",
		Fallback_allowed: fallback_allowed,
		// Games travel over UDP, so every probe measures it too; its
		// answers also stand for the DNS check, which keeps the probe off
		// the DNS Client service.
		Udp_queries: platform.Default_udp_queries,
		Logger:      logger,
	})
	if err != nil {
		return nil, err
	}

	// The match servers and the player's answers about them stay on this
	// machine, beside the state.
	matches, err := matchserver.Open_store(p.root)
	if err != nil {
		logger.Warn("match server answers not read; counting from none", "error", err.Error())
		matches = nil
	}

	var observations *evidence.Collector
	// network_key names the network under the machine by the evidence store's
	// salted hash, for the once-a-day automatic measurement. Without the store
	// there is no salt to hash with, and nothing is measured automatically.
	var network_key func(ctx context.Context) (string, error)
	evidence_store, evidence_err := evidence.Open(filepath.Join(p.root, "connection-evidence.json"))
	if evidence_err != nil {
		logger.Warn("connection evidence store unavailable; connectivity is unaffected")
	} else {
		network_key = func(ctx context.Context) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			material := winplatform.Physical_network_key()
			if material == "" {
				return "", nil
			}
			return evidence_store.Fingerprint(material), nil
		}
		observations = evidence.New(evidence.Config{Store: evidence_store, Client: control_plane, Logger: logger,
			Auth: func(ctx context.Context) (string, error) {
				value, err := secrets.Get(security.Key_device_token)
				return string(value), err
			},
			Capture: func(ctx context.Context) (string, *http.Client, error) {
				return winplatform.Capture_network(ctx, addresses.Current()[0])
			},
		})
	}
	return core.New_application(core.Dependencies{
		Statistics_path:     filepath.Join(p.root, "statistics.json"),
		Analysis_cache_path: filepath.Join(p.root, "analysis.json"),
		// Beside analysis.json: when each network was last measured, by hashed key only.
		Analysis_networks_path: filepath.Join(p.root, "analysis-networks.json"),
		Network_key:            network_key,
		Reach_list_path:        filepath.Join(p.root, "reach-list.json"),
		Evidence:               observations,
		Control_plane:          control_plane,
		Secrets:                secrets,
		Store:                  store,
		Protocols:              registry,
		Prober:                 prober,
		Reach_prober:           reach.New_prober(reach.System_network{}),
		Pinger:                 winplatform.New_pinger(adapter_name, openvpn_adapter_name),
		Game_pinger:            adapter,
		Bus:                    events.New_bus(0),
		Device_name:            device_name(),
		Logger:                 logger,
		Journal:                operations,
		Matches:                matches,
		// What a problem report attaches: the log's tail, a description of
		// the adapters, and the Windows version.
		Log_path: p.log,
		Network_summary: func(ctx context.Context) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return winplatform.Network_summary()
		},
		Os_version: os_version(),
		// Beside state.json: how far the last problem report reached.
		Report_sent_path: filepath.Join(p.root, "report-sent.json"),
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
	if r.stop_watching != nil {
		if err := r.stop_watching(); err != nil {
			r.logger.Warn("stopping the network watcher", "error", err.Error())
		}
		r.network_changes.Stop()
	}
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
	if err := r.operations.Flush(); err != nil {
		r.logger.Warn("the journal was not saved", "error", err.Error())
	}
	r.log_file.Close()
	if err != nil {
		return fmt.Errorf("stopping: %w", err)
	}
	return nil
}
