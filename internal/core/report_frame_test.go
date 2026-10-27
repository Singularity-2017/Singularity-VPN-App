package core

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"singularity-vpn-app/internal/ipc"
)

// draft_frame is the line the pipe would carry for a draft: the draft as the
// result of an ok response, the way the server writes it, and its newline.
func draft_frame(t *testing.T, draft ipc.Problem_draft) int {
	t.Helper()
	result, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := json.Marshal(ipc.Response{Type: ipc.Type_response, Id: strings.Repeat("i", 64), Ok: true, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	return len(frame) + 1
}

// The window reads the draft in one frame, which the pipe bounds. The log is
// in it as text now, so the worst case is a log that doubles when escaped.
func Test_a_worst_case_draft_fits_the_ipc_frame_well(t *testing.T) {
	log := strings.Repeat(`"\`, log_tail_max/2)
	from := log_now
	draft := ipc.Problem_draft{
		Id:          strings.Repeat("f", 32),
		Diagnostics: json.RawMessage(`"` + strings.Repeat("x", diagnostics_max_bytes-2) + `"`),
		Log_bytes:   len(log), Log: log, Log_from: &from,
	}
	if size := draft_frame(t, draft); size >= ipc.Max_frame_bytes/2 {
		t.Fatalf("a worst-case draft is a %d-byte frame; %d is the most the pipe takes and half of it the most this may use", size, ipc.Max_frame_bytes)
	}
}

// Quotes and backslashes double. A "<", an ampersand and a control character
// are six bytes each once JSON has them, so the log is also held to a size
// after escaping.
func Test_a_log_tail_that_grows_when_escaped_is_cut_to_fit_the_frame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	var lines []string
	for i := 0; i < 400; i++ {
		lines = append(lines, log_line(time.Hour-time.Duration(i)*time.Second, strings.Repeat("<", 1000)))
	}
	write_log(t, path, lines...)

	text, from, err := read_log_tail(path, log_now)
	if err != nil {
		t.Fatal(err)
	}
	if text == "" || len(text) > log_tail_max {
		t.Fatalf("tail is %d bytes", len(text))
	}
	if escaped := json_escaped_len(text); escaped > log_tail_escaped_max {
		t.Fatalf("tail is %d bytes once escaped, over %d", escaped, log_tail_escaped_max)
	}
	if !strings.Contains(text, log_now.Add(-time.Hour+399*time.Second).Format("2006-01-02T15:04:05")) {
		t.Fatal("the newest line was not kept")
	}
	if first, _ := log_line_time(strings.SplitN(text, "\n", 2)[0]); !first.Equal(from) {
		t.Fatalf("log_from %v is not the first kept line's time %v", from, first)
	}

	draft := ipc.Problem_draft{
		Id:          strings.Repeat("f", 32),
		Diagnostics: json.RawMessage(`"` + strings.Repeat("x", diagnostics_max_bytes-2) + `"`),
		Log_bytes:   len(text), Log: text, Log_from: &from,
	}
	if size := draft_frame(t, draft); size >= ipc.Max_frame_bytes*3/4 {
		t.Fatalf("the draft is a %d-byte frame, too near the %d the pipe takes", size, ipc.Max_frame_bytes)
	}
}

func Test_a_log_with_bytes_that_are_not_text_is_repaired_before_it_is_measured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	write_log(t, path, "time="+log_now.Add(-time.Minute).Format("2006-01-02T15:04:05.000Z07:00")+" level=INFO msg=\"a\xff\xfeb\"\n")
	text, _, err := read_log_tail(path, log_now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "a�") {
		t.Fatalf("tail = %q", text)
	}
	var round string
	if err := json.Unmarshal(mustMarshal(t, text), &round); err != nil || round != text {
		t.Fatalf("the tail does not survive JSON as it is: %v", err)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
