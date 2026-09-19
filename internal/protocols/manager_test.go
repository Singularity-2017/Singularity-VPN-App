package protocols

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fake_protocol is a minimal Protocol used only to exercise the registry.
type fake_protocol struct {
	name string
}

func (f *fake_protocol) Name() string { return f.name }

func (f *fake_protocol) Connect(ctx context.Context, config Connection_config) error { return nil }

func (f *fake_protocol) Disconnect(ctx context.Context) error { return nil }

func (f *fake_protocol) Status() Status { return Status{} }

func Test_get_returns_the_registered_adapter(t *testing.T) {
	wireguard := &fake_protocol{name: "wireguard"}
	registry, err := New_registry(wireguard)
	if err != nil {
		t.Fatalf("New_registry() error = %v", err)
	}

	got, err := registry.Get("wireguard")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != wireguard {
		t.Fatalf("Get() = %v, want %v", got, wireguard)
	}
}

func Test_get_on_an_unknown_name_is_err_unknown_protocol(t *testing.T) {
	registry, err := New_registry(&fake_protocol{name: "wireguard"})
	if err != nil {
		t.Fatalf("New_registry() error = %v", err)
	}

	_, err = registry.Get("openvpn")
	if !errors.Is(err, Err_unknown_protocol) {
		t.Fatalf("Get() error = %v, want Err_unknown_protocol", err)
	}
}

func Test_duplicate_registration_is_refused(t *testing.T) {
	_, err := New_registry(&fake_protocol{name: "wireguard"}, &fake_protocol{name: "wireguard"})
	if !errors.Is(err, Err_duplicate_protocol) {
		t.Fatalf("New_registry() error = %v, want Err_duplicate_protocol", err)
	}
}

func Test_available_preserves_registration_order(t *testing.T) {
	first := &fake_protocol{name: "wireguard"}
	second := &fake_protocol{name: "openvpn"}
	third := &fake_protocol{name: "shadowsocks"}

	registry, err := New_registry(first, second, third)
	if err != nil {
		t.Fatalf("New_registry() error = %v", err)
	}

	available := registry.Available()
	if len(available) != 3 {
		t.Fatalf("Available() returned %d adapters, want 3", len(available))
	}
	if available[0] != first || available[1] != second || available[2] != third {
		t.Fatalf("Available() = %v, want [%v %v %v]", available, first, second, third)
	}
}

func Test_the_printed_form_of_a_connection_config_does_not_contain_its_data(t *testing.T) {
	config := Connection_config{
		Node_id:  "de-01",
		Protocol: "wireguard",
		Data:     []byte("this is secret key material"),
	}

	printed := config.String()
	if strings.Contains(printed, "secret key material") {
		t.Fatalf("String() leaked Data: %s", printed)
	}
	if !strings.Contains(printed, "de-01") || !strings.Contains(printed, "wireguard") {
		t.Fatalf("String() = %q, want it to name the node and protocol", printed)
	}
}

// var check that fake_protocol satisfies Protocol, so a signature drift here
// is caught before the tests that use it.
var _ Protocol = (*fake_protocol)(nil)
