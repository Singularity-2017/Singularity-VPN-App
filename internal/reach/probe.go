// Package reach measures, from the device and with the tunnel down, what the
// network under it lets through, and judges the measurements into "blocked,
// and by which method". Probing is impure and sits behind Network; judging
// is a pure function over what probing found.
package reach

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// Step_timeout bounds each of the three steps of one probe, and Run_timeout
// bounds a whole run. Three steps at four seconds fit inside the run bound,
// so the run bound only matters when something else delays the probes.
const (
	Step_timeout = 4 * time.Second
	Run_timeout  = 15 * time.Second
)

// Step is how far one step of a probe got.
type Step string

const (
	Step_open       Step = "open"
	Step_blocked    Step = "blocked"
	Step_not_tested Step = "not_tested" // an earlier step failed; nothing is guessed about this one
)

// Failure says how a failed step failed. The set is closed: a caller may
// switch over it.
type Failure string

const (
	Failure_none            Failure = ""
	Failure_no_address      Failure = "no_address"      // dns: no address came back
	Failure_refused         Failure = "refused"         // tcp, tls: the peer or something on the path said no
	Failure_timeout         Failure = "timeout"         // tcp, tls: nothing came back in time
	Failure_reset           Failure = "reset"           // tcp, tls: the connection was torn down, EOF during the handshake included
	Failure_bad_certificate Failure = "bad_certificate" // tls: verification failed -- a stub page or interception
)

// Target is one host and port to probe. Country is set on a control: a host
// inside the caller's own jurisdiction, which says whether the network
// answers at all.
type Target struct {
	Key     string
	Host    string
	Port    int
	Country string
	Label   string
}

// Result is what probing one target found. Failure is set exactly when one
// of the steps is Step_blocked.
type Result struct {
	Target  Target
	Dns     Step
	Tcp     Step
	Tls     Step
	Failure Failure
}

// Network is the part of probing that touches the network. The real one is
// System_network; a test hands in a fake.
type Network interface {
	// Resolve returns the first address the system resolver gives for host,
	// as the node does.
	Resolve(ctx context.Context, host string) (string, error)
	// Dial opens a TCP connection to address and port.
	Dial(ctx context.Context, address string, port int) (net.Conn, error)
	// Handshake runs a TLS handshake over conn with SNI = host, normal
	// certificate verification and a minimum of TLS 1.2. It does not close
	// conn.
	Handshake(ctx context.Context, conn net.Conn, host string) error
}

// Prober runs probes through a Network.
type Prober struct {
	network      Network
	step_timeout time.Duration
	run_timeout  time.Duration
}

// New_prober returns a Prober with the timeouts above.
func New_prober(network Network) *Prober {
	return &Prober{network: network, step_timeout: Step_timeout, run_timeout: Run_timeout}
}

// Probe probes every target at once and returns one Result per target, in
// the order given. It returns an error, and no results, only when ctx ended:
// a run that was cut short by its caller measured nothing worth reporting. A
// run that hit its own bound reports what it found.
func (p *Prober) Probe(ctx context.Context, targets []Target) ([]Result, error) {
	run_ctx, cancel := context.WithTimeout(ctx, p.run_timeout)
	defer cancel()

	results := make([]Result, len(targets))
	var wait sync.WaitGroup
	for i, target := range targets {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[i] = p.probe_one(run_ctx, target)
		}()
	}
	wait.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// probe_one runs dns, tcp and tls in that order, stopping at the first that
// fails.
func (p *Prober) probe_one(ctx context.Context, target Target) Result {
	result := Result{Target: target, Dns: Step_not_tested, Tcp: Step_not_tested, Tls: Step_not_tested}

	dns_ctx, cancel := context.WithTimeout(ctx, p.step_timeout)
	address, err := p.network.Resolve(dns_ctx, target.Host)
	cancel()
	if err != nil || address == "" {
		result.Dns, result.Failure = Step_blocked, Failure_no_address
		return result
	}
	result.Dns = Step_open

	tcp_ctx, cancel := context.WithTimeout(ctx, p.step_timeout)
	conn, err := p.network.Dial(tcp_ctx, address, target.Port)
	cancel()
	if err != nil {
		result.Tcp, result.Failure = Step_blocked, classify(err, false)
		return result
	}
	defer conn.Close()
	result.Tcp = Step_open

	tls_ctx, cancel := context.WithTimeout(ctx, p.step_timeout)
	err = p.network.Handshake(tls_ctx, conn, target.Host)
	cancel()
	if err != nil {
		result.Tls, result.Failure = Step_blocked, classify(err, true)
		return result
	}
	result.Tls = Step_open
	return result
}

// is_timeout reports an error that means "nothing came back in time".
func is_timeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var net_err net.Error
	return errors.As(err, &net_err) && net_err.Timeout()
}
