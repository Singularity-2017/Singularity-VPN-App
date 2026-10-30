package diagnostics

import (
	"context"
	"errors"
	"testing"
	"time"
)

// settling_prober fails its first failures probes and then reports a
// working tunnel, the way a tunnel does whose address and routes Windows
// is still putting in place when the protocol says it is up.
type settling_prober struct {
	failures int
	probes   int
	window   time.Duration
}

func (s *settling_prober) Probe(ctx context.Context) (Sample, error) {
	s.probes++
	if s.probes <= s.failures {
		return Sample{Tunnel_up: true}, nil
	}
	return Sample{Tunnel_up: true, Internet_ok: true}, nil
}

func (s *settling_prober) Settle_window() time.Duration { return s.window }

func Test_verify_waits_for_a_fresh_tunnel_to_settle(t *testing.T) {
	prober := &settling_prober{failures: 2, window: 5 * time.Second}

	if err := Verify(context.Background(), prober); err != nil {
		t.Fatalf("Verify() error = %v, want nil: the tunnel carried traffic on the third probe", err)
	}
	if prober.probes != 3 {
		t.Fatalf("probes = %d, want 3", prober.probes)
	}
}

func Test_verify_gives_up_once_the_settle_window_is_spent(t *testing.T) {
	prober := &settling_prober{failures: 1 << 30, window: 600 * time.Millisecond}

	started := time.Now()
	err := Verify(context.Background(), prober)
	if !errors.Is(err, Err_no_internet) {
		t.Fatalf("Verify() error = %v, want Err_no_internet", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Verify() took %v, want about the 600ms window", elapsed)
	}
	if prober.probes < 2 {
		t.Fatalf("probes = %d, want more than one inside the window", prober.probes)
	}
}

func Test_verify_stops_settling_when_its_context_ends(t *testing.T) {
	prober := &settling_prober{failures: 1 << 30, window: time.Hour}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if err := Verify(ctx, prober); err == nil {
		t.Fatal("Verify() error = nil, want the last failure")
	}
	if ctx.Err() == nil {
		t.Fatal("Verify() returned before its context ended")
	}
}
