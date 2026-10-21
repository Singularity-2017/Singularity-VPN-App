package openvpn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"singularity-vpn-app/internal/protocols"
)

// Name is the protocol name the control plane uses for OpenVPN.
const Name = "openvpn"

// Err_already_connected is what Connect returns while a tunnel is up.
var Err_already_connected = errors.New("openvpn: already connected")

// connect_budget is how long Connect may take. The node's profile lists its
// UDP addresses before its TCP ones, and where UDP is cut each of those
// waits out poll_timeout before the next is tried: three of them, then a
// TCP connection and a TLS handshake.
const (
	connect_budget = 20 * time.Second
	poll_timeout   = "4"
)

// stop_grace is how long openvpn.exe is given to take its routes and
// addresses down after being asked to stop, before it is killed.
const stop_grace = 10 * time.Second

// process is a running openvpn.exe.
type process interface {
	// Lines is its log, one line at a time, closed when the output ends.
	// It must be read to the end, or openvpn.exe blocks writing to it.
	Lines() <-chan string
	// Done is closed once it has exited and everything it held is released.
	Done() <-chan struct{}
	// Stop asks it to take the tunnel down and exit.
	Stop()
	// Kill ends it at once.
	Kill()
}

// starter starts openvpn.exe with these arguments.
type starter func(args []string) (process, error)

// device is the network adapter openvpn.exe runs its tunnel on. It exists
// only while this adapter is connected.
type device interface {
	Close() error
}

type device_factory func() (device, error)

// Adapter is protocols.Protocol over openvpn.exe: the profile goes to a
// file only LocalSystem and administrators can read, and openvpn.exe runs
// on a wintun adapter this service creates for it.
type Adapter struct {
	start        starter
	new_device   device_factory
	device_name  string
	profile_path string
	logger       *slog.Logger
	now          func() time.Time

	// op serialises Connect, Disconnect and the release after openvpn.exe
	// exits on its own: one tunnel's adapter and profile are gone before
	// the next tunnel's are created. It is held across slow work; mu,
	// which Status takes, never is.
	op sync.Mutex

	mu       sync.Mutex
	session  *session
	progress progress
}

type session struct {
	process process
	device  device
	since   time.Time
}

// New_adapter is the adapter with its process and device supplied: the
// real ones on Windows (New_windows_adapter), fakes in tests. A profile
// left at profile_path by a service that did not stop cleanly is removed:
// it holds a private key, and nothing is running from it.
func New_adapter(start starter, new_device device_factory, device_name, profile_path string, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if profile_path != "" {
		os.Remove(profile_path)
	}
	return &Adapter{
		start:        start,
		new_device:   new_device,
		device_name:  device_name,
		profile_path: profile_path,
		logger:       logger,
		now:          time.Now,
	}
}

func (a *Adapter) Name() string { return Name }

// Whole_machine_only implements protocols.Whole_machine: the games-only
// split needs the packet driver, which only the WireGuard path drives.
func (a *Adapter) Whole_machine_only() {}

// Connect_budget implements protocols.Connect_budgeter.
func (a *Adapter) Connect_budget() time.Duration { return connect_budget }

// arguments are what openvpn.exe is started with. The options after the
// profile are this adapter's; check_profile keeps the profile from naming
// any of them, and coming last they would win anyway.
func (a *Adapter) arguments() []string {
	return []string{
		"--config", a.profile_path,
		"--windows-driver", "wintun",
		"--dev-node", a.device_name,
		// Built-in programs only (netsh, route): nothing a profile or a
		// pushed option names is ever run.
		"--script-security", "1",
		// Each address is tried once: past the last one, openvpn.exe exits
		// and the state machine moves on, instead of it retrying forever
		// behind a connection the state machine thinks is lost.
		"--connect-retry-max", "1",
		"--server-poll-timeout", poll_timeout,
		"--resolv-retry", "5",
		"--verb", "3",
		"--suppress-timestamps",
	}
}

// read takes a log line into account and says whether it ended the
// attempt or completed it.
func (a *Adapter) read(line string) (fatal string, up bool) {
	a.logger.Debug("openvpn", "line", redact(line))
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progress.read(line)
	return a.progress.fatal, a.progress.phase == phase_up
}

// Connect brings the tunnel up and returns once openvpn.exe reports it
// set up, or once it cannot be: a fatal line, openvpn.exe exiting, or ctx
// ending. Every failure leaves nothing behind -- no process, no adapter,
// no profile on disk.
func (a *Adapter) Connect(ctx context.Context, config protocols.Connection_config) error {
	a.op.Lock()
	defer a.op.Unlock()

	a.mu.Lock()
	busy := a.session != nil
	if !busy {
		a.progress = progress{}
	}
	a.mu.Unlock()
	if busy {
		return Err_already_connected
	}
	if err := check_profile(config.Data); err != nil {
		return err
	}
	a.mu.Lock()
	a.progress.carried.read_profile(config.Data)
	a.mu.Unlock()
	if len(config.Split.Games) > 0 {
		a.logger.Info("openvpn carries the whole machine; games-only mode is not available over it")
	}

	if err := os.WriteFile(a.profile_path, config.Data, 0o600); err != nil {
		return fmt.Errorf("openvpn: writing the profile: %w", err)
	}
	dev, err := a.new_device()
	if err != nil {
		os.Remove(a.profile_path)
		return fmt.Errorf("openvpn: creating the adapter: %w", err)
	}
	proc, err := a.start(a.arguments())
	if err != nil {
		dev.Close()
		os.Remove(a.profile_path)
		return fmt.Errorf("openvpn: starting openvpn.exe: %w", err)
	}
	s := &session{process: proc, device: dev}

	// fail ends the attempt. The log goes on being read in the background
	// -- its one reader from here on -- so openvpn.exe never blocks
	// writing it while it is being stopped.
	fail := func(cause error) error {
		go drain(proc.Lines())
		a.release(s)
		return cause
	}

	lines := proc.Lines()
	for {
		select {
		case <-ctx.Done():
			return fail(fmt.Errorf("openvpn: %s: %w", a.report_or("did not come up in time"), ctx.Err()))
		case line, ok := <-lines:
			if !ok {
				lines = nil // the output ended; Done says why
				continue
			}
			fatal, up := a.read(line)
			if fatal != "" {
				return fail(fmt.Errorf("openvpn: %s", fatal))
			}
			if up {
				// The keys are in memory now, and a restart inside
				// openvpn.exe does not read the file again: the private
				// key does not stay on disk for the life of the tunnel.
				os.Remove(a.profile_path)
				a.mu.Lock()
				s.since = a.now()
				a.session = s
				a.mu.Unlock()
				go a.watch(s, lines)
				return nil
			}
		case <-proc.Done():
			// What it printed before exiting may not have been read yet,
			// and the last line is usually the reason.
			if lines != nil {
				timeout := time.After(time.Second)
			drain:
				for {
					select {
					case line, ok := <-lines:
						if !ok {
							break drain
						}
						a.read(line)
					case <-timeout:
						break drain
					}
				}
			}
			a.mu.Lock()
			fatal := a.progress.fatal
			a.mu.Unlock()
			if fatal != "" {
				return fail(fmt.Errorf("openvpn: %s", fatal))
			}
			return fail(fmt.Errorf("openvpn: openvpn.exe exited: %s", a.report_or("before the tunnel came up")))
		}
	}
}

func drain(lines <-chan string) {
	for range lines {
	}
}

// report_or says how far the attempt got, for an error.
func (a *Adapter) report_or(otherwise string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if report := a.progress.report(); report != "" {
		return report
	}
	return otherwise
}

// watch follows a tunnel that is up, as the log's one reader. When
// openvpn.exe exits on its own it releases what the session held, so the
// next Connect starts clean; after Disconnect it has nothing left to do.
func (a *Adapter) watch(s *session, lines <-chan string) {
	for line := range lines {
		a.logger.Debug("openvpn", "line", redact(line))
		a.mu.Lock()
		if a.session == s {
			a.progress.read(line)
		}
		a.mu.Unlock()
	}
	<-s.process.Done()

	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	mine := a.session == s
	if mine {
		a.session = nil
	}
	last := a.progress.fatal
	a.mu.Unlock()
	if !mine {
		return
	}
	a.logger.Warn("openvpn.exe exited with the tunnel up", "last", last)
	a.release(s)
}

// release stops openvpn.exe -- asking first, so it removes its routes and
// addresses -- and then removes the adapter and the profile. The caller
// holds op.
func (a *Adapter) release(s *session) {
	s.process.Stop()
	select {
	case <-s.process.Done():
	case <-time.After(stop_grace):
		a.logger.Warn("openvpn.exe did not stop when asked; killing it")
		s.process.Kill()
		<-s.process.Done()
	}
	if err := s.device.Close(); err != nil {
		a.logger.Warn("closing the openvpn adapter", "err", err)
	}
	if err := os.Remove(a.profile_path); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.logger.Warn("removing the openvpn profile", "err", err)
	}
}

// Disconnect takes the tunnel down. With none up it does nothing.
func (a *Adapter) Disconnect(ctx context.Context) error {
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	s := a.session
	a.session = nil
	a.mu.Unlock()
	if s != nil {
		a.release(s)
	}
	return nil
}

// Status reports whether the tunnel is up, and over which transport.
func (a *Adapter) Status() protocols.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		return protocols.Status{}
	}
	status := protocols.Status{Running: true, Since: a.session.since}
	if a.progress.transport != "" {
		status.Detail = "over " + a.progress.transport
	}
	return status
}

// Transport_report implements protocols.Transport_reporter.
func (a *Adapter) Transport_report() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.progress.report()
}
