package wireguard

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func Test_default_resolver_resolves_host_and_port(t *testing.T) {
	r := Default_resolver{
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			if host != "vpn.example.org" {
				t.Fatalf("lookup called with host %q, want vpn.example.org", host)
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.5")}, nil
		},
	}

	got, err := r.Resolve(context.Background(), "vpn.example.org:51080")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := netip.MustParseAddrPort("203.0.113.5:51080")
	if got != want {
		t.Fatalf("Resolve() = %v, want %v", got, want)
	}
}

func Test_default_resolver_uses_the_first_address_when_several_come_back(t *testing.T) {
	r := Default_resolver{
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.5"), netip.MustParseAddr("203.0.113.6")}, nil
		},
	}

	got, err := r.Resolve(context.Background(), "vpn.example.org:51080")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Addr().String() != "203.0.113.5" {
		t.Fatalf("Resolve() = %v, want the first address", got)
	}
}

func Test_default_resolver_rejects_a_value_that_is_not_host_port(t *testing.T) {
	r := Default_resolver{lookup: func(context.Context, string) ([]netip.Addr, error) {
		t.Fatalf("lookup must not run when host:port does not even parse")
		return nil, nil
	}}
	if _, err := r.Resolve(context.Background(), "not-a-host-port"); err == nil {
		t.Fatalf("Resolve: want an error")
	}
}

func Test_default_resolver_rejects_an_invalid_port(t *testing.T) {
	r := Default_resolver{lookup: func(context.Context, string) ([]netip.Addr, error) {
		t.Fatalf("lookup must not run when the port is invalid")
		return nil, nil
	}}
	if _, err := r.Resolve(context.Background(), "vpn.example.org:not-a-port"); err == nil {
		t.Fatalf("Resolve: want an error")
	}
}

func Test_default_resolver_propagates_a_lookup_failure(t *testing.T) {
	r := Default_resolver{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return nil, errors.New("no such host")
	}}
	if _, err := r.Resolve(context.Background(), "vpn.example.org:51080"); err == nil {
		t.Fatalf("Resolve: want an error")
	}
}

func Test_default_resolver_refuses_a_lookup_that_returns_nothing(t *testing.T) {
	r := Default_resolver{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return nil, nil
	}}
	if _, err := r.Resolve(context.Background(), "vpn.example.org:51080"); err == nil {
		t.Fatalf("Resolve: want an error when lookup returns no addresses")
	}
}
