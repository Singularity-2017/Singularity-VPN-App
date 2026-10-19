package reach

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"
)

// --- judging -----------------------------------------------------------

func open_result(key, country string) Result {
	return Result{Target: Target{Key: key, Host: key + ".example", Port: 443, Country: country, Label: key},
		Dns: Step_open, Tcp: Step_open, Tls: Step_open}
}

func failed_at(r Result, step Method, failure Failure) Result {
	r.Failure = failure
	switch step {
	case Method_dns:
		r.Dns, r.Tcp, r.Tls = Step_blocked, Step_not_tested, Step_not_tested
	case Method_tcp:
		r.Tcp, r.Tls = Step_blocked, Step_not_tested
	case Method_tls:
		r.Tls = Step_blocked
	}
	return r
}

func Test_judging_attributes_a_failure_only_when_the_controls_prove_the_network_answers(t *testing.T) {
	good_control := open_result("bank", "RU")
	dead_control := failed_at(open_result("bank", "RU"), Method_dns, Failure_no_address)
	tls_only_control := failed_at(open_result("bank", "RU"), Method_tls, Failure_reset) // tcp open: the network answered

	cases := []struct {
		name         string
		results      []Result
		want_control Control
		want         []Verdict
		want_methods []Method
	}{
		{"all open with a control",
			[]Result{good_control, open_result("web", "")},
			Control_ok, []Verdict{Verdict_open, Verdict_open}, []Method{"", ""}},
		{"dns blocked, control ok",
			[]Result{good_control, failed_at(open_result("web", ""), Method_dns, Failure_no_address)},
			Control_ok, []Verdict{Verdict_open, Verdict_blocked}, []Method{"", Method_dns}},
		{"tcp blocked, control ok",
			[]Result{good_control, failed_at(open_result("web", ""), Method_tcp, Failure_timeout)},
			Control_ok, []Verdict{Verdict_open, Verdict_blocked}, []Method{"", Method_tcp}},
		{"tls blocked, control ok",
			[]Result{good_control, failed_at(open_result("web", ""), Method_tls, Failure_reset)},
			Control_ok, []Verdict{Verdict_open, Verdict_blocked}, []Method{"", Method_tls}},
		{"a control that got tcp open is enough",
			[]Result{dead_control, tls_only_control, failed_at(open_result("web", ""), Method_tcp, Failure_refused)},
			Control_ok, []Verdict{Verdict_no_answer, Verdict_no_answer, Verdict_blocked}, []Method{"", "", Method_tcp}},
		{"controls failed: no network, nothing blocked",
			[]Result{dead_control, failed_at(open_result("web", ""), Method_tcp, Failure_timeout), open_result("other", "")},
			Control_failed, []Verdict{Verdict_no_answer, Verdict_no_network, Verdict_open}, []Method{"", "", ""}},
		{"no controls: cannot be attributed",
			[]Result{failed_at(open_result("web", ""), Method_dns, Failure_no_address), open_result("other", "")},
			Control_absent, []Verdict{Verdict_no_answer, Verdict_open}, []Method{"", ""}},
		{"a control is never blocked",
			[]Result{good_control, failed_at(open_result("bank2", "RU"), Method_tcp, Failure_refused)},
			Control_ok, []Verdict{Verdict_open, Verdict_no_answer}, []Method{"", ""}},
		{"nothing to judge", nil, Control_absent, []Verdict{}, []Method{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Judge(c.results)
			if got.Control != c.want_control || len(got.Checks) != len(c.want) {
				t.Fatalf("Judge = control %q with %d checks; want %q with %d", got.Control, len(got.Checks), c.want_control, len(c.want))
			}
			for i, check := range got.Checks {
				if check.Verdict != c.want[i] || check.Method != c.want_methods[i] {
					t.Errorf("check %d (%s) = %q/%q; want %q/%q", i, check.Target.Key, check.Verdict, check.Method, c.want[i], c.want_methods[i])
				}
				if check.Control != (c.results[i].Target.Country != "") {
					t.Errorf("check %d: Control = %v", i, check.Control)
				}
			}
		})
	}
}

func Test_judging_never_calls_an_unfinished_probe_open_or_blocked(t *testing.T) {
	r := open_result("web", "")
	r.Tls = Step_not_tested // no step failed, yet not everything is open
	got := Judge([]Result{open_result("bank", "RU"), r})
	if got.Checks[1].Verdict != Verdict_no_answer || got.Checks[1].Method != Method_none {
		t.Fatalf("an unfinished probe = %q/%q; want no_answer", got.Checks[1].Verdict, got.Checks[1].Method)
	}
}

// --- probing -----------------------------------------------------------

type fake_host struct {
	resolve_err   error
	dial_err      error
	handshake_err error
}

// fake_network answers from a table and records every call, so a test can
// assert which steps ran. Nothing here touches a real network.
type fake_network struct {
	hosts map[string]fake_host

	mu    sync.Mutex
	calls []string
	conns []net.Conn
}

func (f *fake_network) record(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}

func (f *fake_network) Resolve(ctx context.Context, host string) (string, error) {
	f.record("resolve " + host)
	if err := f.hosts[host].resolve_err; err != nil {
		return "", err
	}
	return "addr:" + host, nil
}

func (f *fake_network) Dial(ctx context.Context, address string, port int) (net.Conn, error) {
	f.record(fmt.Sprintf("dial %s:%d", address, port))
	host := address[len("addr:"):]
	if err := f.hosts[host].dial_err; err != nil {
		return nil, err
	}
	near, far := net.Pipe()
	f.mu.Lock()
	f.conns = append(f.conns, near, far)
	f.mu.Unlock()
	return near, nil
}

func (f *fake_network) Handshake(ctx context.Context, conn net.Conn, host string) error {
	f.record("handshake " + host)
	return f.hosts[host].handshake_err
}

func (f *fake_network) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func probe_target(t *testing.T, host fake_host) (Result, *fake_network) {
	t.Helper()
	network := &fake_network{hosts: map[string]fake_host{"a.example": host}}
	results, err := New_prober(network).Probe(context.Background(), []Target{{Key: "a", Host: "a.example", Port: 443}})
	if err != nil || len(results) != 1 {
		t.Fatalf("Probe = %+v, %v", results, err)
	}
	return results[0], network
}

func Test_a_failed_step_leaves_the_later_steps_untested(t *testing.T) {
	cert_error := &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}
	cases := []struct {
		name         string
		host         fake_host
		want         Result
		want_dial    bool
		want_shaking bool
	}{
		{"dns fails", fake_host{resolve_err: errors.New("no such host")},
			Result{Dns: Step_blocked, Tcp: Step_not_tested, Tls: Step_not_tested, Failure: Failure_no_address}, false, false},
		{"tcp refused", fake_host{dial_err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}},
			Result{Dns: Step_open, Tcp: Step_blocked, Tls: Step_not_tested, Failure: Failure_refused}, true, false},
		{"tcp times out", fake_host{dial_err: context.DeadlineExceeded},
			Result{Dns: Step_open, Tcp: Step_blocked, Tls: Step_not_tested, Failure: Failure_timeout}, true, false},
		{"tls reset by EOF", fake_host{handshake_err: io.EOF},
			Result{Dns: Step_open, Tcp: Step_open, Tls: Step_blocked, Failure: Failure_reset}, true, true},
		{"tls bad certificate", fake_host{handshake_err: cert_error},
			Result{Dns: Step_open, Tcp: Step_open, Tls: Step_blocked, Failure: Failure_bad_certificate}, true, true},
		{"everything open", fake_host{},
			Result{Dns: Step_open, Tcp: Step_open, Tls: Step_open}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, network := probe_target(t, c.host)
			got.Target = Target{}
			if got != c.want {
				t.Errorf("Probe = %+v; want %+v", got, c.want)
			}
			if network.called("dial addr:a.example:443") != c.want_dial || network.called("handshake a.example") != c.want_shaking {
				t.Errorf("calls = %v; want dial %v, handshake %v", network.calls, c.want_dial, c.want_shaking)
			}
		})
	}
}

func Test_a_probe_closes_the_connection_it_opened(t *testing.T) {
	_, network := probe_target(t, fake_host{})
	near := network.conns[0]
	if _, err := near.Write([]byte{0}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writing to the probed connection = %v; want it closed", err)
	}
}

func Test_every_target_is_probed_and_results_keep_the_order_given(t *testing.T) {
	network := &fake_network{hosts: map[string]fake_host{
		"b.example": {resolve_err: errors.New("no such host")},
	}}
	targets := []Target{
		{Key: "a", Host: "a.example", Port: 443},
		{Key: "b", Host: "b.example", Port: 443},
		{Key: "c", Host: "c.example", Port: 8443},
	}
	results, err := New_prober(network).Probe(context.Background(), targets)
	if err != nil || len(results) != 3 {
		t.Fatalf("Probe = %+v, %v", results, err)
	}
	for i, target := range targets {
		if results[i].Target != target {
			t.Errorf("result %d is for %+v; want %+v", i, results[i].Target, target)
		}
	}
	if results[1].Dns != Step_blocked || results[0].Tls != Step_open || !network.called("dial addr:c.example:8443") {
		t.Fatalf("results = %+v; calls = %v", results, network.calls)
	}
}

// slow_network never answers; it returns when its context ends.
type slow_network struct{ fake_network }

func (s *slow_network) Dial(ctx context.Context, address string, port int) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func Test_a_step_that_never_answers_ends_at_its_timeout(t *testing.T) {
	network := &slow_network{fake_network{hosts: map[string]fake_host{}}}
	prober := New_prober(network)
	prober.step_timeout = 20 * time.Millisecond
	results, err := prober.Probe(context.Background(), []Target{{Key: "a", Host: "a.example", Port: 443}})
	if err != nil || results[0].Tcp != Step_blocked || results[0].Failure != Failure_timeout || results[0].Tls != Step_not_tested {
		t.Fatalf("Probe = %+v, %v; want tcp blocked by timeout", results, err)
	}
}

func Test_a_run_its_caller_cancelled_reports_nothing(t *testing.T) {
	network := &slow_network{fake_network{hosts: map[string]fake_host{}}}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	results, err := New_prober(network).Probe(ctx, []Target{{Key: "a", Host: "a.example", Port: 443}})
	if err == nil || results != nil {
		t.Fatalf("Probe = %+v, %v; want an error and no results", results, err)
	}
}

func Test_errors_fall_into_the_closed_failure_set(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		handshake bool
		want      Failure
	}{
		{"refused", &net.OpError{Err: syscall.ECONNREFUSED}, false, Failure_refused},
		{"windows refused", &net.OpError{Err: syscall.Errno(10061)}, false, Failure_refused},
		{"reset", &net.OpError{Err: syscall.ECONNRESET}, true, Failure_reset},
		{"windows reset", &net.OpError{Err: syscall.Errno(10054)}, true, Failure_reset},
		{"deadline", context.DeadlineExceeded, false, Failure_timeout},
		{"unexpected EOF", io.ErrUnexpectedEOF, true, Failure_reset},
		{"hostname mismatch", x509.HostnameError{Host: "a.example"}, true, Failure_bad_certificate},
		{"unrecognised at tcp", errors.New("network is unreachable"), false, Failure_timeout},
		{"unrecognised at tls", errors.New("tls: handshake failure"), true, Failure_reset},
	}
	for _, c := range cases {
		if got := classify(c.err, c.handshake); got != c.want {
			t.Errorf("%s: classify = %q; want %q", c.name, got, c.want)
		}
	}
}
