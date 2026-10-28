package core

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var log_now = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// log_line is a line as slog's text handler writes it, d before log_now.
func log_line(d time.Duration, message string) string {
	return fmt.Sprintf("time=%s level=INFO msg=%q\n", log_now.Add(-d).Format("2006-01-02T15:04:05.000Z07:00"), message)
}

func write_log(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func Test_redact_log_line(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"nothing to hide", `time=T level=INFO msg="connected" node_id=node-1`, `time=T level=INFO msg="connected" node_id=node-1`},
		{"a token", `msg=x token=abc123 next=1`, `msg=x token=<REDACTED> next=1`},
		{"a quoted value with spaces", `msg=x password="correct horse battery" next=1`, `msg=x password=<REDACTED> next=1`},
		{"a quoted value with an escaped quote", `msg=x secret="a \"b\" c" next=1`, `msg=x secret=<REDACTED> next=1`},
		{"a prefix and a suffix on the key", `device_token=abc tokens=def`, `device_token=<REDACTED> tokens=<REDACTED>`},
		{"private key both ways", `private_key=abc PrivateKey=def`, `private_key=<REDACTED> PrivateKey=<REDACTED>`},
		{"capitals", `TOKEN=abc Authorization=xyz`, `TOKEN=<REDACTED> Authorization=<REDACTED>`},
		{"an empty value, as slog writes it", `token="" next=1`, `token=<REDACTED> next=1`},
		{"a colon and spaces", `token : abc next`, `token : <REDACTED> next`},
		{"a WireGuard config line", `PrivateKey = YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo=`, `PrivateKey = <REDACTED>`},
		{"a basic authorization header", `Authorization: Basic dXNlcjpwYXNz`, `Authorization: <REDACTED>`},
		{"a bearer authorization header", `Authorization: Bearer abc.def`, `Authorization: <REDACTED>`},
		{"the other keys", `psk=a preshared_key=b api_key=c apikey=d passwd=e passphrase=f cookie=g`,
			`psk=<REDACTED> preshared_key=<REDACTED> api_key=<REDACTED> apikey=<REDACTED> passwd=<REDACTED> passphrase=<REDACTED> cookie=<REDACTED>`},
		{"a number in JSON", `{"token":12345}`, `{"token":<REDACTED>}`},
		{"JSON inside a value slog quoted", `body="{\"token\":\"abc\",\"user\":\"x\"}"`, `body="{\"token\":<REDACTED>,\"user\":\"x\"}"`},
		{"addresses of both families", `dial 203.0.113.7:51820 via 2001:db8::1 at 12:30:45`, `dial …:51820 via … at 12:30:45`},
		{"a bearer anywhere", `msg="request failed: Bearer abc.def-ghi_123 was refused"`, `msg="request failed: Bearer <REDACTED> was refused"`},
		{"a bearer in any case", `got bearer   abc123 back`, `got bearer   <REDACTED> back`},
		{"json", `body={"token":"abc","user":"x","password" : "p\"q"}`, `body={"token":<REDACTED>,"user":"x","password" : <REDACTED>}`},
		{"a word that only contains the letters of a key", `msg="a tokenizer ran" key=1`, `msg="a tokenizer ran" key=1`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redact_log_line(tc.in); got != tc.want {
				t.Fatalf("\n got  %s\n want %s", got, tc.want)
			}
		})
	}
}

func Test_redact_attrs_works_on_a_copy(t *testing.T) {
	original := map[string]string{"token": "abc", "error": "Bearer xyz refused", "node_id": "n1"}
	got := redact_attrs(original)
	if got["token"] != redacted || got["error"] != "Bearer "+redacted+" refused" || got["node_id"] != "n1" {
		t.Fatalf("redacted = %v", got)
	}
	if original["token"] != "abc" || original["error"] != "Bearer xyz refused" {
		t.Fatalf("the journal's own map was changed: %v", original)
	}
	if redact_attrs(nil) != nil {
		t.Fatal("no attributes became some")
	}
}

func Test_redact_attrs_runs_the_whole_redactor_over_values(t *testing.T) {
	got := redact_attrs(map[string]string{
		"error": `dial failed: token=abc123 Authorization: Basic dXNlcjpwYXNz via 2001:db8::1`,
		"psk":   "abc",
	})
	if got["psk"] != redacted {
		t.Errorf("psk = %q", got["psk"])
	}
	for _, leaked := range []string{"abc123", "dXNlcjpwYXNz", "2001:db8"} {
		if strings.Contains(got["error"], leaked) {
			t.Errorf("%q survived in %q", leaked, got["error"])
		}
	}
}

// slog_record writes one record through slog's text handler, the way the
// service's log is written, at d before log_now.
func slog_record(t *testing.T, d time.Duration, level slog.Level, message string, attrs ...slog.Attr) string {
	t.Helper()
	var out bytes.Buffer
	record := slog.NewRecord(log_now.Add(-d), level, message, 0)
	record.AddAttrs(attrs...)
	if err := slog.NewTextHandler(&out, nil).Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

const fake_wireguard_key = "WGZha2Uta2V5LW5vdC1hLXJlYWwtd2lyZWd1YXJkLWtleQ=="

// What the service really writes -- slog's own quoting, escapes and all --
// loses its secrets to the redactor and keeps the words around them.
func Test_secrets_in_real_slog_output_are_redacted(t *testing.T) {
	cases := []struct {
		name    string
		message string
		attrs   []slog.Attr
		secrets []string // none may survive
		kept    []string // all must
	}{
		{"a token", "request", []slog.Attr{slog.String("token", "s3cr3t-value"), slog.Int("attempt", 2)}, []string{"s3cr3t-value"}, []string{"attempt=2", "token=" + redacted}},
		{"a password with spaces and quotes", "sign in", []slog.Attr{slog.String("password", `correct "horse" battery`)}, []string{"correct", "horse", "battery"}, []string{"password=" + redacted}},
		{"a key under a group", "wireguard", []slog.Attr{slog.Group("wg", slog.String("psk", "pskvalue123"))}, []string{"pskvalue123"}, []string{"wg.psk=" + redacted}},
		{"every spelling of a key", "keys", []slog.Attr{
			slog.String("device_token", "v1"), slog.String("preshared_key", "v2"), slog.String("PresharedKey", "v3"), slog.String("api_key", "v4"),
			slog.String("ApiKey", "v5"), slog.String("passwd", "v6"), slog.String("passphrase", "v7"), slog.String("cookie", "v8"), slog.String("client_secret", "v9"),
		}, []string{"=v1", "=v2", "=v3", "=v4", "=v5", "=v6", "=v7", "=v8", "=v9"}, []string{"device_token=" + redacted, "cookie=" + redacted}},
		{"a header in a value", "sent", []slog.Attr{slog.String("header", "Authorization: Basic dXNlcjpwYXNzd29yZA==")}, []string{"dXNlcjpwYXNzd29yZA"}, []string{"sent", "Authorization: " + redacted}},
		{"a bearer in an error", "refused", []slog.Attr{slog.Any("error", fmt.Errorf("request refused: Bearer eyJhbGciOi.payload.signature"))}, []string{"eyJhbGciOi"}, []string{"Bearer " + redacted}},
		{"a WireGuard config line in an error", "config rejected", []slog.Attr{slog.Any("error",
			fmt.Errorf("wireguard: %q appears before any [Interface] or [Peer] section", "PrivateKey = "+fake_wireguard_key))},
			[]string{fake_wireguard_key, "WGZha2Uta2V5"}, []string{"PrivateKey = " + redacted, "appears before any [Interface] or [Peer] section"}},
		{"a whole config in an error", "config rejected", []slog.Attr{slog.String("error",
			"[Interface]\nPrivateKey = "+fake_wireguard_key+"\nAddress = 10.0.0.2/32\n[Peer]\nPresharedKey = "+fake_wireguard_key+"\n")},
			[]string{fake_wireguard_key}, []string{"[Interface]", "[Peer]"}},
		{"JSON in a value", "response", []slog.Attr{slog.String("body", `{"token":"abc123","user":"x","password":"p w"}`)}, []string{"abc123", "p w"}, []string{`user\":\"x`}},
		{"a number in JSON", "response", []slog.Attr{slog.String("body", `{"token":987654321}`)}, []string{"987654321"}, []string{"response"}},
		{"nothing secret", "connected", []slog.Attr{slog.String("node_id", "node-1"), slog.String("protocol", "wireguard")}, nil, []string{"node_id=node-1", "protocol=wireguard"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := slog_record(t, time.Minute, slog.LevelWarn, tc.message, tc.attrs...)
			got := redact_log_line(line)
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Errorf("%q survived:\n%s", secret, got)
				}
			}
			for _, kept := range tc.kept {
				if !strings.Contains(got, kept) {
					t.Errorf("%q was lost:\n%s", kept, got)
				}
			}
			if !strings.HasPrefix(got, "time=2026-10-27T11:59:00.000Z level=WARN msg=") {
				t.Errorf("the line's own prefix was changed:\n%s", got)
			}
			if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
				t.Errorf("the line was broken:\n%q", got)
			}
		})
	}
}

func Test_addresses_in_real_slog_output_are_masked_in_the_log_tail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	write_log(t, path,
		slog_record(t, 2*time.Minute, slog.LevelInfo, "endpoint", slog.String("addr", "203.0.113.7:51820"), slog.String("v6", "2001:db8::1")),
		slog_record(t, time.Minute, slog.LevelWarn, "dial failed", slog.Any("error", fmt.Errorf("dial tcp [2001:db8:85a3::8a2e:370:7334]:443: i/o timeout; fe80::1%%eth0; 198.51.100.9")), slog.Int("n", 3)),
	)
	text, from, err := read_log_tail(path, log_now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"203.0.113", "51820", "2001:db8", "8a2e", "fe80::1", "198.51.100"} {
		// The port of the first one stays: it is not an address.
		if address == "51820" {
			continue
		}
		if strings.Contains(text, address) {
			t.Errorf("%q survived:\n%s", address, text)
		}
	}
	if !strings.Contains(text, "addr=…:51820") || !strings.Contains(text, "i/o timeout") || !strings.Contains(text, "n=3") {
		t.Errorf("the words around the addresses were lost:\n%s", text)
	}
	if !from.Equal(log_now.Add(-2 * time.Minute)) {
		t.Errorf("log_from = %v: masking disturbed the timestamp", from)
	}
	if !strings.HasPrefix(text, "time=2026-10-27T11:58:00.000Z ") {
		t.Errorf("the first timestamp was changed:\n%s", text)
	}
}

func Test_read_log_tail_keeps_the_last_two_hours_redacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	write_log(t, path,
		log_line(3*time.Hour, "too old"),
		log_line(2*time.Hour+time.Second, "just too old"),
		log_line(time.Hour, "an hour ago token=abc"),
		log_line(time.Minute, "recent"),
	)
	text, from, err := read_log_tail(path, log_now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "too old") {
		t.Fatalf("a line outside the window was kept:\n%s", text)
	}
	if !strings.Contains(text, "an hour ago") || !strings.Contains(text, "recent") {
		t.Fatalf("a line inside the window was dropped:\n%s", text)
	}
	if !strings.Contains(text, "token=<REDACTED>") || strings.Contains(text, "token=abc") {
		t.Fatalf("a token was kept:\n%s", text)
	}
	if want := log_now.Add(-time.Hour); !from.Equal(want) {
		t.Fatalf("log starts at %v, want %v", from, want)
	}
	if strings.Count(text, "\n") != 2 {
		t.Fatalf("expected two lines:\n%s", text)
	}
}

func Test_read_log_tail_reads_the_rolled_log_only_when_the_window_reaches_it(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	write_log(t, path+".1", log_line(3*time.Hour, "three hours"), log_line(90*time.Minute, "before the roll"))

	// service.log started a minute ago: the window reaches back past it.
	write_log(t, path, log_line(time.Minute, "after the roll"))
	text, from, err := read_log_tail(path, log_now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "before the roll") || !strings.Contains(text, "after the roll") || strings.Contains(text, "three hours") {
		t.Fatalf("tail =\n%s", text)
	}
	if !from.Equal(log_now.Add(-90 * time.Minute)) {
		t.Fatalf("log starts at %v", from)
	}
	if strings.Index(text, "before the roll") > strings.Index(text, "after the roll") {
		t.Fatal("the rolled log came after the current one")
	}

	// service.log already reaches back past the window: .1 is older still.
	write_log(t, path, log_line(3*time.Hour, "old start"), log_line(time.Minute, "recent"))
	text, _, err = read_log_tail(path, log_now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "before the roll") || strings.Contains(text, "old start") || !strings.Contains(text, "recent") {
		t.Fatalf("tail =\n%s", text)
	}
}

func Test_read_log_tail_without_a_log_is_empty_not_an_error(t *testing.T) {
	text, from, err := read_log_tail(filepath.Join(t.TempDir(), "service.log"), log_now, time.Time{})
	if err != nil || text != "" || !from.IsZero() {
		t.Fatalf("tail = %q, %v, %v", text, from, err)
	}
	// Only the rolled one is there.
	dir := t.TempDir()
	write_log(t, filepath.Join(dir, "service.log.1"), log_line(time.Minute, "rolled"))
	text, _, err = read_log_tail(filepath.Join(dir, "service.log"), log_now, time.Time{})
	if err != nil || !strings.Contains(text, "rolled") {
		t.Fatalf("tail = %q, %v", text, err)
	}
}

func Test_read_log_tail_is_capped_and_keeps_the_newest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	var lines []string
	for i := 0; i < 4000; i++ {
		lines = append(lines, log_line(time.Hour-time.Duration(i)*time.Millisecond*500, fmt.Sprintf("line %04d %s", i, strings.Repeat("x", 60))))
	}
	write_log(t, path, lines...)
	text, from, err := read_log_tail(path, log_now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > log_tail_max || len(text) < log_tail_max-300 {
		t.Fatalf("tail is %d bytes, want just under %d", len(text), log_tail_max)
	}
	if !strings.Contains(text, "line 3999") || strings.Contains(text, "line 0000") {
		t.Fatal("the newest lines were not the ones kept")
	}
	// The first line is whole: the cut fell between lines, not inside one.
	if !strings.HasPrefix(text, "time=") {
		t.Fatalf("the tail starts mid-line: %.60q", text)
	}
	if first, _ := log_line_time(strings.SplitN(text, "\n", 2)[0]); !first.Equal(from) {
		t.Fatalf("log_from %v is not the first line's time %v", from, first)
	}
}

func Test_read_log_tail_is_capped_after_redaction_lengthens_it(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	var lines []string
	// "token=1" becomes "token=<REDACTED>": eight bytes longer a line.
	for i := 0; i < 5000; i++ {
		lines = append(lines, log_line(time.Minute, fmt.Sprintf("n%04d token=1", i)))
	}
	write_log(t, path, lines...)
	text, _, err := read_log_tail(path, log_now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > log_tail_max {
		t.Fatalf("tail is %d bytes, over %d", len(text), log_tail_max)
	}
}

func Test_read_file_tail_starts_on_a_line(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("aaaa\nbbbb\ncccc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		max       int64
		want      string
		truncated bool
	}{
		{100, "aaaa\nbbbb\ncccc\n", false},
		{15, "aaaa\nbbbb\ncccc\n", false},
		{10, "bbbb\ncccc\n", true}, // begins exactly on a line: kept whole
		{9, "cccc\n", true},        // begins mid "bbbb\n": that line is dropped
		{5, "cccc\n", true},        // begins exactly on a line: kept whole
		{4, "", true},              // no whole line fits
	}
	for _, tc := range cases {
		got, truncated, err := read_file_tail(path, tc.max)
		if err != nil || string(got) != tc.want || truncated != tc.truncated {
			t.Errorf("max %d: got %q, truncated=%v, %v; want %q, %v", tc.max, got, truncated, err, tc.want, tc.truncated)
		}
	}
}

// The service holds service.log open for appending while a report reads it.
func Test_the_log_can_be_read_while_the_service_appends_to_it(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.WriteString(log_line(time.Minute, "first")); err != nil {
		t.Fatal(err)
	}
	text, _, err := read_log_tail(path, log_now, time.Time{})
	if err != nil || !strings.Contains(text, "first") {
		t.Fatalf("tail = %q, %v", text, err)
	}
	if _, err := writer.WriteString(log_line(time.Second, "second")); err != nil {
		t.Fatalf("the reader got in the writer's way: %v", err)
	}
}
