package connection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/diagnostics"
	"singularity-vpn-app/internal/protocols"
)

func Test_fault_error_and_unwrap(t *testing.T) {
	underlying := errors.New("boom")
	fault := &Fault{Kind: Fault_timeout, Op: "connect", Err: underlying}

	if !errors.Is(fault, underlying) {
		t.Fatalf("errors.Is(fault, underlying) = false, want true through Unwrap")
	}
	if !strings.Contains(fault.Error(), "boom") {
		t.Fatalf("Error() = %q, want it to mention the underlying error", fault.Error())
	}
}

func Test_kind_of_recognizes_a_fault_and_nothing_else(t *testing.T) {
	fault := &Fault{Kind: Fault_verification, Op: "connect", Err: errors.New("down")}

	kind, ok := Kind_of(fault)
	if !ok || kind != Fault_verification {
		t.Fatalf("Kind_of(fault) = %v, %v, want Fault_verification, true", kind, ok)
	}

	if _, ok := Kind_of(errors.New("not a fault")); ok {
		t.Fatal("Kind_of(plain error) ok = true, want false")
	}
}

func Test_kind_of_sees_through_wrapping(t *testing.T) {
	fault := &Fault{Kind: Fault_authentication, Op: "connect", Err: errors.New("no")}
	wrapped := errors.Join(errors.New("context"), fault)

	kind, ok := Kind_of(wrapped)
	if !ok || kind != Fault_authentication {
		t.Fatalf("Kind_of(wrapped) = %v, %v, want Fault_authentication, true", kind, ok)
	}
}

func Test_classify_control_plane_error_unauthorized_is_authentication(t *testing.T) {
	err := &api.Error{Status: 401, Code: api.Code_unauthorized}
	fault := classify_control_plane_error("recommend", err)
	if fault.Kind != Fault_authentication {
		t.Fatalf("classify_control_plane_error() kind = %v, want Fault_authentication", fault.Kind)
	}
}

func Test_classify_control_plane_error_from_recommend_is_recommendation(t *testing.T) {
	err := &api.Error{Status: 500, Code: api.Code_internal_error}
	fault := classify_control_plane_error("recommend", err)
	if fault.Kind != Fault_recommendation {
		t.Fatalf("classify_control_plane_error() kind = %v, want Fault_recommendation", fault.Kind)
	}
}

func Test_classify_control_plane_error_from_provisioning_is_configuration(t *testing.T) {
	err := &api.Error{Status: 409, Code: api.Code_client_limit_reached}
	fault := classify_control_plane_error("create_profile", err)
	if fault.Kind != Fault_configuration {
		t.Fatalf("classify_control_plane_error() kind = %v, want Fault_configuration", fault.Kind)
	}
}

func Test_classify_control_plane_error_transport_failure_is_control_plane_unavailable(t *testing.T) {
	fault := classify_control_plane_error("recommend", errors.New("dial tcp: connection refused"))
	if fault.Kind != Fault_control_plane_unavailable {
		t.Fatalf("classify_control_plane_error() kind = %v, want Fault_control_plane_unavailable", fault.Kind)
	}
}

func Test_classify_control_plane_error_incomplete_response_is_control_plane_unavailable(t *testing.T) {
	fault := classify_control_plane_error("fetch_config", api.Err_incomplete_response)
	if fault.Kind != Fault_control_plane_unavailable {
		t.Fatalf("classify_control_plane_error() kind = %v, want Fault_control_plane_unavailable", fault.Kind)
	}
}

func Test_classify_protocol_lookup_error_is_protocol_unavailable(t *testing.T) {
	fault := classify_protocol_lookup_error("connect", protocols.Err_unknown_protocol)
	if fault.Kind != Fault_protocol_unavailable {
		t.Fatalf("classify_protocol_lookup_error() kind = %v, want Fault_protocol_unavailable", fault.Kind)
	}
}

func Test_classify_connect_error_is_protocol_connection(t *testing.T) {
	fault := classify_connect_error("connect", errors.New("handshake failed"))
	if fault.Kind != Fault_protocol_connection {
		t.Fatalf("classify_connect_error() kind = %v, want Fault_protocol_connection", fault.Kind)
	}
}

func Test_classify_connect_error_deadline_exceeded_is_timeout(t *testing.T) {
	fault := classify_connect_error("connect", context.DeadlineExceeded)
	if fault.Kind != Fault_timeout {
		t.Fatalf("classify_connect_error() kind = %v, want Fault_timeout", fault.Kind)
	}
}

func Test_classify_verify_error_is_verification(t *testing.T) {
	fault := classify_verify_error("connect", diagnostics.Err_tunnel_down)
	if fault.Kind != Fault_verification {
		t.Fatalf("classify_verify_error() kind = %v, want Fault_verification", fault.Kind)
	}

	fault = classify_verify_error("connect", diagnostics.Err_no_internet)
	if fault.Kind != Fault_verification {
		t.Fatalf("classify_verify_error() kind = %v, want Fault_verification", fault.Kind)
	}
}

func Test_classify_verify_error_deadline_exceeded_is_timeout(t *testing.T) {
	fault := classify_verify_error("connect", context.DeadlineExceeded)
	if fault.Kind != Fault_timeout {
		t.Fatalf("classify_verify_error() kind = %v, want Fault_timeout", fault.Kind)
	}
}

// A Fault must never carry config bytes: api.Vpn_config.Config,
// api.Profile.Config and protocols.Connection_config.Data are the tunnel's
// key material.
func Test_a_fault_from_a_failed_connect_does_not_contain_the_config_text(t *testing.T) {
	secret := "-----BEGIN PRIVATE KEY super secret bytes-----"
	connect_err := errors.New("adapter refused: handshake timed out")
	fault := classify_connect_error("connect", connect_err)

	if strings.Contains(fault.Error(), secret) {
		t.Fatalf("Fault.Error() = %q, must never contain config text", fault.Error())
	}
	rendered := fmt.Sprintf("%v", fault)
	if strings.Contains(rendered, secret) {
		t.Fatalf("fmt.Sprintf(%%v, fault) = %q, must never contain config text", rendered)
	}
}
