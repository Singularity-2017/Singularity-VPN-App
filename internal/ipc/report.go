package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Command_problem_draft asks the service to assemble what a problem report
// will carry -- diagnostics and a log tail -- and keep it, so the person can
// read it before deciding to send it. Command_feedback then names the draft.
const Command_problem_draft = "problem_draft"

// Limits on what the window may hand the service about the programs it saw.
const (
	max_app_uses      = 200
	max_app_exe_runes = 128
)

// Failure_signal says the connection has kept failing lately, for the window
// to offer a problem report. It is absent from the status unless the failures
// outweigh what a network change or the person's own disconnect explains.
type Failure_signal struct {
	Count  int       `json:"count"` // failures counted
	First  time.Time `json:"first"`
	Last   time.Time `json:"last"`
	Blips  int       `json:"blips,omitempty"`  // connection_blip events
	Lost   int       `json:"lost,omitempty"`   // connection_lost, switch_failed
	Failed int       `json:"failed,omitempty"` // a connect, a retry or the service itself failing
}

// App_use is a program that was in the foreground. The window process sees
// the desktop; the service does not, so the window reports it. Exe is a base
// name only, never a path.
type App_use struct {
	Exe  string    `json:"exe"` // base name only, e.g. "cs2.exe"
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Problem_draft_params carries what only the window knows.
type Problem_draft_params struct {
	Apps []App_use `json:"apps,omitempty"`
}

// Validate refuses more programs than the window keeps, and a name that is
// empty, too long or is a path: a path names a folder in somebody's profile.
func (p Problem_draft_params) Validate() error {
	if len(p.Apps) > max_app_uses {
		return fmt.Errorf("ipc: problem draft: more than %d programs", max_app_uses)
	}
	for _, app := range p.Apps {
		if app.Exe == "" || !utf8.ValidString(app.Exe) || utf8.RuneCountInString(app.Exe) > max_app_exe_runes {
			return errors.New("ipc: problem draft: a program name is empty or too long")
		}
		if strings.ContainsAny(app.Exe, `/\`) {
			return errors.New("ipc: problem draft: a program name must not be a path")
		}
	}
	return nil
}

// Problem_draft is what Command_problem_draft answers with.
type Problem_draft struct {
	Id          string          `json:"id"`          // 32 hex chars
	Diagnostics json.RawMessage `json:"diagnostics"` // exactly what will be sent
	Log_bytes   int             `json:"log_bytes"`   // size of the attached log tail
	// Log is the attached log tail itself, redacted: the exact text that is
	// held and will be sent, so the person reads what leaves the machine.
	Log string `json:"log,omitempty"`
	// Log_from is a pointer for the reason Status_result.Since is: a zero
	// time would be sent as the year one, and "unknown" must be absent.
	Log_from *time.Time `json:"log_from,omitempty"` // time of its first line, when known
}

// Problem_service is the optional side of Service that assembles a problem
// report, found by shape like Feedback_service.
type Problem_service interface {
	Problem_draft(context.Context, Problem_draft_params) (Problem_draft, error)
}

// Problem_draft asks the service to assemble a problem report's attachments.
func (c *Client) Problem_draft(ctx context.Context, params Problem_draft_params) (Problem_draft, error) {
	var result Problem_draft
	if err := params.Validate(); err != nil {
		return result, err
	}
	resp, err := c.call(ctx, Command_problem_draft, params)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}
