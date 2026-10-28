package core

import (
	"encoding/json"
	"os"
	"time"
)

// A problem report carries the journal and the log up to the moment its draft
// was made. Once the control plane has accepted one, that moment is kept, and
// the next report carries only what came after it: the same lines sent twice
// are the same evidence counted twice, and a stranger's log read twice over.
// The moment is kept in its own small file, not in state.json, whose one
// writer is the connection manager.

// report_sent_limit bounds reading the file; it holds one time.
const report_sent_limit = 1 << 10

// report_sent is the file's shape.
type report_sent struct {
	Until time.Time `json:"until"`
}

// reported_until is the moment up to which an earlier report has carried the
// journal and the log, or the zero time when none has. A moment later than
// now is a clock that went back, and is not trusted to hide what came since.
// An unreadable or corrupt file is logged by category only and read as no
// report: at worst a report sends again what one sent before.
func (a *Application) reported_until(now time.Time) time.Time {
	if a.report_sent_path == "" {
		return time.Time{}
	}
	a.report_sent_mu.Lock()
	defer a.report_sent_mu.Unlock()
	sent, ok := a.read_report_sent()
	if !ok || sent.Until.After(now) {
		return time.Time{}
	}
	return sent.Until
}

// record_reported notes that a report carried the journal and the log up to
// until. A report accepted after a later one does not move the moment back.
func (a *Application) record_reported(until time.Time) {
	if a.report_sent_path == "" {
		return
	}
	a.report_sent_mu.Lock()
	defer a.report_sent_mu.Unlock()
	if sent, ok := a.read_report_sent(); ok && !until.After(sent.Until) {
		return
	}
	data, err := json.Marshal(report_sent{Until: until})
	if err == nil {
		err = write_small_file(a.report_sent_path, ".report-sent-*.tmp", data)
	}
	if err != nil {
		a.logger.Warn("report sent file could not be saved")
	}
}

// read_report_sent reads the file; the caller holds report_sent_mu.
func (a *Application) read_report_sent() (report_sent, bool) {
	data, err := read_small_file(a.report_sent_path, report_sent_limit)
	if os.IsNotExist(err) {
		return report_sent{}, false
	}
	if err != nil {
		a.logger.Warn("report sent file unavailable")
		return report_sent{}, false
	}
	var sent report_sent
	if json.Unmarshal(data, &sent) != nil {
		a.logger.Warn("report sent file invalid")
		return report_sent{}, false
	}
	return sent, true
}
