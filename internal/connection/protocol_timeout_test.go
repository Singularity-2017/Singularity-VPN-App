package connection

import (
	"context"
	"testing"
	"time"

	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/protocols"
)

// budgeted_protocol is a fake_protocol that names its own connect budget.
type budgeted_protocol struct {
	*fake_protocol
	budget time.Duration
}

func (b budgeted_protocol) Connect_budget() time.Duration { return b.budget }

func Test_a_protocol_that_needs_longer_gets_its_own_budget_and_no_less_than_the_default(t *testing.T) {
	plain := &fake_protocol{name: "wireguard"}
	cases := []struct {
		name       string
		configured time.Duration
		protocol   protocols.Protocol
		want       time.Duration
	}{
		{"no budget", 5 * time.Second, plain, 5 * time.Second},
		{"a longer budget", 5 * time.Second, budgeted_protocol{plain, 25 * time.Second}, 25 * time.Second},
		{"a shorter budget", 5 * time.Second, budgeted_protocol{plain, time.Second}, 5 * time.Second},
		{"no limit configured", 0, budgeted_protocol{plain, 25 * time.Second}, 0},
	}
	for _, c := range cases {
		if got := protocol_timeout(c.configured, c.protocol); got != c.want {
			t.Errorf("%s: protocol_timeout = %v, want %v", c.name, got, c.want)
		}
	}
}

// deadline_protocol is a budgeted_protocol that records how long each
// Connect was given.
type deadline_protocol struct {
	budgeted_protocol
	given []time.Duration
}

func (d *deadline_protocol) Connect(ctx context.Context, config protocols.Connection_config) error {
	if deadline, ok := ctx.Deadline(); ok {
		d.given = append(d.given, time.Until(deadline))
	} else {
		d.given = append(d.given, 0)
	}
	return d.fake_protocol.Connect(ctx, config)
}

// The retry on the same node gives the protocol its budget too: OpenVPN
// held to the default would never reach its TCP addresses on a retry.
func Test_the_first_connect_and_the_retry_both_get_the_protocol_s_budget(t *testing.T) {
	env := new_test_env(t)
	budgeted := &deadline_protocol{budgeted_protocol: budgeted_protocol{env.protocol, 20 * time.Second}}
	env.protocols.wrap = func(*fake_protocol) protocols.Protocol { return budgeted }
	manager := env.build()
	if err := manager.Connect(context.Background(), Connect_request{Mode: Mode_auto}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	env.prober.set([]prober_response{{sample: diagnostics.Sample{Tunnel_up: false}}, {sample: healthy_sample}})
	manager.Tick(context.Background(), env.clock.advance(5*time.Second))

	if len(budgeted.given) != 2 {
		t.Fatalf("Connect calls = %d, want 2 (the connect and the retry)", len(budgeted.given))
	}
	for i, given := range budgeted.given {
		if given < 15*time.Second {
			t.Errorf("Connect %d was given %v, want the 20s budget", i+1, given)
		}
	}
}
