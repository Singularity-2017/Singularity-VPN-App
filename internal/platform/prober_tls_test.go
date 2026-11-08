package platform

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"singularity-vpn-app/internal/diagnostics"
)

func Test_TLS_probe_rejects_local_TCP_acceptance(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		config := base_config()
		config.Attempts = 1
		config.Attempt_timeout = 50 * time.Millisecond
		config.Lookup = lookup_ok("192.0.2.1")
		config.Verify_tls = always_up
		var peer net.Conn
		config.Dial = func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			peer = server
			if !stalled {
				server.Close()
			}
			return client, nil
		}
		prober, err := New_network_prober(config)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = prober.Probe(ctx)
		cancel()
		peer.Close()
		if !errors.Is(err, diagnostics.Err_no_internet) {
			t.Fatalf("local TCP acknowledgement counted as Internet access (stalled=%v): %v", stalled, err)
		}
	}
}

func Test_TLS_probe_requires_a_trusted_peer(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	config := base_config()
	config.Attempts = 1
	config.Lookup = lookup_ok("192.0.2.1")
	config.Verify_tls = always_up
	config.Dial_target = server.Listener.Addr().String()
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = prober.Probe(context.Background())
	if !errors.Is(err, diagnostics.Err_no_internet) {
		t.Fatalf("untrusted peer counted as Internet access: %v", err)
	}
}

func Test_TLS_requirement_survives_proxy_exit_during_dial(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	config := base_config()
	config.Attempts = 1
	config.Lookup = lookup_ok("192.0.2.1")
	config.Verify_tls = running.Load
	config.Dial = func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		peer.Close()
		running.Store(false)
		return client, nil
	}
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prober.Probe(context.Background()); !errors.Is(err, diagnostics.Err_no_internet) {
		t.Fatalf("proxy exit weakened the probe: %v", err)
	}
}

func Test_TLS_probe_never_falls_back_to_another_interface(t *testing.T) {
	config := base_config()
	config.Attempts = 1
	config.Lookup = lookup_ok("192.0.2.1")
	config.Verify_tls = always_up
	config.Dial = func(context.Context, string, string) (net.Conn, error) {
		t.Error("unbound connection attempted")
		return nil, errors.New("wrong interface")
	}
	config.Verified_dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("proxy interface unavailable")
	}
	config.Fallback_target = "192.0.2.2:443"
	config.Fallback_allowed = always_up
	config.Udp_queries = 1
	config.Dial_udp = config.Dial
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prober.Probe(context.Background()); !errors.Is(err, diagnostics.Err_no_internet) {
		t.Fatalf("unavailable proxy produced a sample: %v", err)
	}
}

func Test_TLS_probe_accepts_a_trusted_response_on_its_bound_transport(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	config := base_config()
	config.Attempts = 1
	config.Lookup = lookup_ok("192.0.2.1")
	config.Verify_tls = always_up
	config.Dial_target = server.Listener.Addr().String()
	config.Dial = func(context.Context, string, string) (net.Conn, error) {
		t.Error("unbound dial used")
		return nil, errors.New("unbound")
	}
	var called atomic.Bool
	config.Verified_dial = func(ctx context.Context, network, target string) (net.Conn, error) {
		called.Store(true)
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
	prober, err := New_network_prober(config)
	if err != nil {
		t.Fatal(err)
	}
	prober.tls_roots = x509.NewCertPool()
	prober.tls_roots.AddCert(server.Certificate())
	sample, err := prober.Probe(context.Background())
	if err != nil || !called.Load() || !sample.Internet_ok || sample.Rtt <= 0 {
		t.Fatalf("trusted bound TLS probe did not succeed: sample=%+v error=%v bound=%v", sample, err, called.Load())
	}
}
