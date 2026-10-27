package ipc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const synthetic_draft_id = "0123456789abcdef0123456789abcdef"

func Test_problem_draft_params_validation(t *testing.T) {
	too_many := make([]App_use, 201)
	for i := range too_many {
		too_many[i] = App_use{Exe: "a.exe"}
	}
	cases := map[string]struct {
		params Problem_draft_params
		ok     bool
	}{
		"none":              {Problem_draft_params{}, true},
		"a program":         {Problem_draft_params{Apps: []App_use{{Exe: "cs2.exe"}}}, true},
		"two hundred":       {Problem_draft_params{Apps: too_many[:200]}, true},
		"over two hundred":  {Problem_draft_params{Apps: too_many}, false},
		"empty name":        {Problem_draft_params{Apps: []App_use{{Exe: ""}}}, false},
		"long name":         {Problem_draft_params{Apps: []App_use{{Exe: strings.Repeat("a", 129)}}}, false},
		"longest name":      {Problem_draft_params{Apps: []App_use{{Exe: strings.Repeat("я", 128)}}}, true},
		"forward slash":     {Problem_draft_params{Apps: []App_use{{Exe: "games/cs2.exe"}}}, false},
		"backslash":         {Problem_draft_params{Apps: []App_use{{Exe: `C:\games\cs2.exe`}}}, false},
		"invalid utf-8":     {Problem_draft_params{Apps: []App_use{{Exe: "a\xffb.exe"}}}, false},
		"second one is bad": {Problem_draft_params{Apps: []App_use{{Exe: "a.exe"}, {Exe: `b\c.exe`}}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tc.params.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func Test_feedback_draft_is_only_for_a_problem_and_only_32_lowercase_hex(t *testing.T) {
	cases := map[string]struct {
		params Feedback_params
		ok     bool
	}{
		"problem with a draft":   {Feedback_params{Kind: "problem", Message: "m", Draft: synthetic_draft_id}, true},
		"problem without":        {Feedback_params{Kind: "problem", Message: "m"}, true},
		"idea with a draft":      {Feedback_params{Kind: "idea", Message: "m", Draft: synthetic_draft_id}, false},
		"upper case":             {Feedback_params{Kind: "problem", Message: "m", Draft: strings.ToUpper(synthetic_draft_id)}, false},
		"too short":              {Feedback_params{Kind: "problem", Message: "m", Draft: synthetic_draft_id[:31]}, false},
		"too long":               {Feedback_params{Kind: "problem", Message: "m", Draft: synthetic_draft_id + "0"}, false},
		"not hex":                {Feedback_params{Kind: "problem", Message: "m", Draft: strings.Repeat("g", 32)}, false},
		"a path in place of one": {Feedback_params{Kind: "problem", Message: "m", Draft: strings.Repeat("../", 11)}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tc.params.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

type problem_test_service struct {
	*fake_service
	got Problem_draft_params
}

func (s *problem_test_service) Problem_draft(_ context.Context, params Problem_draft_params) (Problem_draft, error) {
	s.got = params
	from := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	return Problem_draft{Id: synthetic_draft_id, Diagnostics: json.RawMessage(`{"schema":1}`), Log_bytes: 42, Log_from: &from}, nil
}

func Test_problem_draft_pipe_validation_and_round_trip(t *testing.T) {
	service := &problem_test_service{fake_service: new_fake_service()}
	client, _ := serve_pair(t, service)
	apps := []App_use{{Exe: "cs2.exe", From: time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 27, 11, 5, 0, 0, time.UTC)}}
	got, err := client.Problem_draft(context.Background(), Problem_draft_params{Apps: apps})
	if err != nil || got.Id != synthetic_draft_id || string(got.Diagnostics) != `{"schema":1}` || got.Log_bytes != 42 || got.Log_from == nil {
		t.Fatalf("draft = %+v, %v", got, err)
	}
	if len(service.got.Apps) != 1 || service.got.Apps[0].Exe != "cs2.exe" {
		t.Fatalf("the service was handed %+v", service.got)
	}

	// Bypass the client validator to prove the service boundary validates too.
	resp, err := client.call(context.Background(), Command_problem_draft, Problem_draft_params{Apps: []App_use{{Exe: `C:\x\y.exe`}}})
	if err != nil {
		t.Fatal(err)
	}
	var result Problem_draft
	if decode_result(resp, &result) == nil {
		t.Fatal("a program path was accepted at the service boundary")
	}

	unsupported, _ := serve_pair(t, new_fake_service())
	if _, err := unsupported.Problem_draft(context.Background(), Problem_draft_params{}); err == nil {
		t.Fatal("a service without problem reports answered a draft")
	}
}

func Test_problem_draft_leaves_an_unknown_log_start_out_of_the_json(t *testing.T) {
	raw, err := json.Marshal(Problem_draft{Id: synthetic_draft_id, Diagnostics: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "log_from") || strings.Contains(string(raw), "0001") {
		t.Fatalf("an unknown moment was sent: %s", raw)
	}
}
