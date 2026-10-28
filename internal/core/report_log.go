package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"singularity-vpn-app/internal/journal"
)

// A problem report carries the newest part of the service log: enough to see
// what the service did around the failure, small enough to send. The log
// holds what the service says about itself, never what a person does, and is
// redacted below before it is kept.
const (
	// log_tail_window is how far back the attached log reaches.
	log_tail_window = 2 * time.Hour
	// log_tail_max is the most of it that is attached; the newest is kept.
	log_tail_max = 192 << 10
	// log_tail_escaped_max bounds the tail once JSON has escaped it. A quote
	// or a backslash doubles; a "<", an ampersand or a control character
	// becomes six bytes. With the diagnostics (diagnostics_max_bytes) this
	// keeps a draft's frame well under ipc.Max_frame_bytes whatever the log
	// holds.
	log_tail_escaped_max = 512 << 10
)

// redacted stands where a secret was.
const redacted = "<REDACTED>"

// secret_key matches a key name that holds a secret: the name itself, or a
// name with a prefix or a suffix ("device_token", "tokens", "wg.psk").
const secret_key = `[\w.-]*(?:private_?key|preshared_?key|api_?key|passphrase|passwd|password|token|secret|authorization|cookie|psk)[\w.-]*`

// secret_json finds a secret key and its value in JSON, bare or inside a
// value slog quoted:
//
//	"token":"abc"  "token":12      JSON
//	\"token\":\"abc\"              JSON inside a value slog quoted
//
// A value that is not a string runs to the comma or bracket that ends it.
var secret_json = regexp.MustCompile(`(?i)(` + secret_key + `)(\\?"[ \t]*:[ \t]*)` +
	`(?:"(?:[^"\\]|\\.)*"|\\"(?:[^\\]|\\[^"])*?\\"|[^\s,}\]"\\]+)`)

// secret_pair finds a secret key and its value in the other spellings a log
// carries:
//
//	token=abc                      slog's text handler, and a config line
//	PrivateKey = abc=              a WireGuard config line, spaces and all
//	Authorization: Basic abc       a header; the scheme is part of the value
//	password="a b \"c\""           slog's quoting
//
// The value is a quoted string, an escaped-quoted one, or a word that may
// follow a scheme. A word stops at a quote that closes something around it
// and at an escaped line break -- a config that slog quoted keeps its next
// line -- but not at any other backslash, which a password may hold. A key
// with nothing after it has nothing to hide and is left alone.
var secret_pair = regexp.MustCompile(`(?i)(` + secret_key + `)([ \t]*[=:][ \t]*)` +
	`(?:"(?:[^"\\]|\\.)*"|\\"(?:[^\\]|\\[^"])*?\\"|(?:(?:basic|bearer|digest|negotiate|ntlm)[ \t]+)?(?:[^\s"\\]|\\[^"nr])+)`)

var (
	// bearer finds what follows "Bearer ", whichever key it sat under.
	bearer = regexp.MustCompile(`(?i)(\bbearer[ \t]+)[^\s"']+`)
	// secret_name is secret_key alone, for a key that stands apart from its
	// value, as in a journal entry's attributes.
	secret_name = regexp.MustCompile(`(?i)^` + secret_key + `$`)
)

// redact_log_line takes the secrets and the addresses out of a piece of the
// log: a secret becomes <REDACTED>, an IPv4 or IPv6 address the ellipsis the
// journal uses. It is a net under the code that already keeps both out of the
// log, not a licence to put them there.
func redact_log_line(line string) string {
	line = secret_json.ReplaceAllString(line, "${1}${2}"+redacted)
	line = secret_pair.ReplaceAllString(line, "${1}${2}"+redacted)
	line = bearer.ReplaceAllString(line, "${1}"+redacted)
	return journal.Mask_addresses(line)
}

// redact_attrs does the same for a journal entry's attributes, on a copy: the
// map belongs to the journal.
func redact_attrs(attrs map[string]string) map[string]string {
	if attrs == nil {
		return nil
	}
	out := make(map[string]string, len(attrs))
	for key, value := range attrs {
		if secret_name.MatchString(key) {
			out[key] = redacted
			continue
		}
		out[key] = redact_log_line(value)
	}
	return out
}

// log_record is one log line and the lines that continue it. slog's text
// handler quotes a value with a newline in it, so a continuation is rare, but
// a log that was written by anything else should not lose its tail.
type log_record struct {
	at   time.Time
	text string
}

// parse_log_records splits a log into records by the leading time=. Lines
// before the first one that has a time carry none to place them by and are
// dropped.
func parse_log_records(data []byte) []log_record {
	var records []log_record
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if at, ok := log_line_time(line); ok {
			records = append(records, log_record{at: at, text: line + "\n"})
			continue
		}
		if n := len(records); n > 0 {
			records[n-1].text += line + "\n"
		}
	}
	return records
}

// log_line_time reads the time= slog's text handler puts first on a line.
func log_line_time(line string) (time.Time, bool) {
	const prefix = "time="
	if !strings.HasPrefix(line, prefix) {
		return time.Time{}, false
	}
	value := line[len(prefix):]
	if end := strings.IndexByte(value, ' '); end >= 0 {
		value = value[:end]
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	return at, err == nil
}

// read_log_tail returns the part of the log at path (and, when the window
// reaches past it, of path+".1" that came before) whose lines are within
// log_tail_window of now and later than after -- what an earlier report has
// not sent -- redacted and at most log_tail_max bytes with the newest kept,
// and the time of its first line. A log that is not there is an empty tail,
// not an error: nothing in the report depends on it.
func read_log_tail(path string, now, after time.Time) (string, time.Time, error) {
	cutoff := now.Add(-log_tail_window)
	wanted := func(at time.Time) bool { return !at.Before(cutoff) && at.After(after) }
	current, truncated, err := read_file_tail(path, log_tail_max)
	if err != nil {
		return "", time.Time{}, err
	}
	records := parse_log_records(current)
	// service.log.1 is the log before the last rollover. It matters only if
	// service.log is whole -- started after it -- and does not already
	// reach back past what is wanted by itself.
	if !truncated && (len(records) == 0 || wanted(records[0].at)) {
		older, _, err := read_file_tail(path+".1", log_tail_max)
		if err != nil {
			return "", time.Time{}, err
		}
		records = append(parse_log_records(older), records...)
	}

	kept := records[:0:0]
	for _, record := range records {
		if wanted(record.at) {
			record.text = redact_log_line(strings.ToValidUTF8(record.text, "�"))
			kept = append(kept, record)
		}
	}
	// Redaction can lengthen a line, so the caps are applied to what is kept:
	// the raw size, and the size once JSON has escaped it. The oldest go first.
	escaped := make([]int, len(kept))
	total, total_escaped := 0, 0
	for i, record := range kept {
		escaped[i] = json_escaped_len(record.text)
		total += len(record.text)
		total_escaped += escaped[i]
	}
	for len(kept) > 0 && (total > log_tail_max || total_escaped > log_tail_escaped_max) {
		total -= len(kept[0].text)
		total_escaped -= escaped[0]
		kept, escaped = kept[1:], escaped[1:]
	}
	if len(kept) == 0 {
		return "", time.Time{}, nil
	}
	var text strings.Builder
	for _, record := range kept {
		text.WriteString(record.text)
	}
	return text.String(), kept[0].at, nil
}

// json_escaped_len is the length of s as a JSON string, without its quotes.
func json_escaped_len(s string) int {
	encoded, err := json.Marshal(s)
	if err != nil {
		return 6 * len(s) // never for a string made valid UTF-8; the worst case if it were
	}
	return len(encoded) - 2
}

// read_file_tail reads the last max bytes of a file, from the start of a line:
// a first line cut in half is dropped. truncated says the file had more. A
// file that does not exist reads as empty.
func read_file_tail(path string, max int64) ([]byte, bool, error) {
	file, err := open_shared(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	size := info.Size()
	start := int64(0)
	if size > max {
		start = size - max
	}
	// One byte more than needed, when there is one: if it is a newline the
	// tail begins exactly on a line and nothing is dropped.
	read_from := start
	if start > 0 {
		read_from = start - 1
	}
	buffer := make([]byte, size-read_from)
	if _, err := file.ReadAt(buffer, read_from); err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if start == 0 {
		return buffer, false, nil
	}
	if buffer[0] == '\n' {
		return buffer[1:], true, nil
	}
	if cut := bytes.IndexByte(buffer, '\n'); cut >= 0 {
		return buffer[cut+1:], true, nil
	}
	return nil, true, nil
}
