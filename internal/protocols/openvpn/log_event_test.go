package openvpn

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func Test_event_logging_keeps_labels_and_discards_payloads(t *testing.T) {
	var output bytes.Buffer
	a := New_adapter(nil, nil, "", "", slog.New(slog.NewTextHandler(&output, nil)))
	a.log_event("AUTH_FAILED secret-password 192.0.2.10")
	a.log_event("PUSH: Received control message: 'PUSH_REPLY,route-gateway 10.29.8.1,ping 10,ping-restart 60,private-key secret-password'")
	for i := 0; i < 200; i++ {
		a.log_event("TLS Error: unknown opcode received from [AF_INET]192.0.2.10:50443 op=0x19")
		a.log_event("TLS Error: cannot locate HMAC in incoming packet from [AF_INET]192.0.2.10:50443")
	}
	a.log_event("TLS Error: TLS handshake failed")
	a.log_event("TLS Error: TLS handshake failed")
	a.log_event("SIGUSR1[soft,ping-restart] received secret-password")
	a.log_event("Initialization Sequence Completed")
	got := output.String()
	for _, forbidden := range []string{"secret-password", "192.0.2.10", "PUSH_REPLY", "private-key", "event=\"TLS Error\""} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("payload or decoy noise leaked: %s", got)
		}
	}
	if !strings.Contains(got, "AUTH_FAILED") || !strings.Contains(got, "TLS handshake failed") || !strings.Contains(got, "ping-restart") || !strings.Contains(got, "Initialization Sequence Completed") {
		t.Fatalf("missing event: %s", got)
	}
	if count := strings.Count(got, "ping-restart"); count != 1 {
		t.Fatalf("ping-restart logged %d times, want 1 (only on SIGUSR1, not on PUSH_REPLY): %s", count, got)
	}
	if count := strings.Count(got, "TLS handshake failed"); count != 1 {
		t.Fatalf("TLS handshake failed logged %d times, want 1 (deduplicated): %s", count, got)
	}
}
