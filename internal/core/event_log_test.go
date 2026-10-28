package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"singularity-vpn-app/internal/connection"
	"singularity-vpn-app/internal/events"
	"singularity-vpn-app/internal/recovery"
)

func attr_map(attrs []any) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(attrs); i += 2 {
		out[fmt.Sprint(attrs[i])] = fmt.Sprint(attrs[i+1])
	}
	return out
}

// A switch says where it goes, and a failed one why: "Переключиться не
// удалось" with nothing more left the owner guessing for 55 minutes
// (2026-10-28). The node and protocol of the session are the one being left.
func Test_the_log_says_where_a_switch_goes_and_why_it_failed(t *testing.T) {
	session := connection.Session{Id: "s1", Node_id: "de-01", Protocol: "wireguard", Tunnel: "vpn"}
	started := attr_map(event_attrs(events.Event{Kind: events.Kind_switch_started, At: time.Now(), Session_id: "s1",
		Data: recovery.Target{Node_id: "de-01", Protocol: "openvpn"}}, session, true))
	if started["to_protocol"] != "openvpn" || started["to_node_id"] != "de-01" || started["protocol"] != "wireguard" {
		t.Errorf("switch_started attrs = %v; want to_protocol openvpn, to_node_id de-01, protocol wireguard", started)
	}
	failed := attr_map(event_attrs(events.Event{Kind: events.Kind_switch_failed, At: time.Now(), Session_id: "s1",
		Data: &connection.Fault{Kind: connection.Fault_protocol_connection, Op: "switch", Err: errors.New("openvpn: no connection within 30s")}}, session, true))
	if !strings.Contains(failed["error"], "openvpn: no connection within 30s") {
		t.Errorf("switch_failed attrs = %v; want the reason", failed)
	}
}

// A nil Fault inside an event is not an error to print: the logger runs on
// the service's own goroutine, and a panic there would stop the service.
func Test_a_nil_fault_in_an_event_logs_no_error(t *testing.T) {
	var fault *connection.Fault
	attrs := attr_map(event_attrs(events.Event{Kind: events.Kind_error, Session_id: "s1", Data: fault}, connection.Session{}, false))
	if _, ok := attrs["error"]; ok {
		t.Errorf("attrs = %v; want no error for a nil fault", attrs)
	}
}
