package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"singularity-vpn-app/internal/ipc"
)

const account_fixture = `{"user_id":"u1","username":"synthetic","plan":"fast","client_limit":3,"subscription":{"active":true,"expires_at":"2027-01-01T00:00:00Z"},"wallet":0,"referral_code":"invite","referrer":{"username":"inviter","token":"SECRET"},"network_score":0,"sponsored_place":false,"token":"SECRET","clients":[{"config":"SECRET","expires_at":"2030-01-01T00:00:00Z"},{"expires_at":"2026-01-01T00:00:00Z"}]}`

func Test_feedback_authenticated_allowlist_and_receipt(t *testing.T) {
	for _, receipt := range []string{`{"accepted":true,"code":"R-TEST","context":"SECRET"}`, `{}`, `null`, `{"accepted":false,"code":"R-TEST"}`, `{"accepted":true,"code":null}`} {
		t.Run(receipt, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/feedback" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("incorrect authenticated feedback request")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 2 || body["kind"] != "idea" || body["message"] != "Synthetic idea" {
					t.Errorf("unsafe payload: %v", body)
				}
				write_success(w, receipt)
			}))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL})
			got, err := client.Feedback(context.Background(), "synthetic-token", ipc.Feedback_params{Kind: "idea", Message: " Synthetic idea "})
			if strings.Contains(receipt, `"context"`) {
				if err != nil || !got.Accepted || got.Code != "R-TEST" {
					t.Fatalf("receipt = %+v, %v", got, err)
				}
			} else if !errors.Is(err, Err_incomplete_response) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func Test_feedback_validation_and_auth_failure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED","message":"unauthorized"}}`))
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	for _, params := range []ipc.Feedback_params{{Kind: "bug", Message: "test"}, {Kind: "idea", Message: " "}, {Kind: "problem", Message: strings.Repeat("a", 2001)}} {
		if _, err := client.Feedback(context.Background(), "token", params); err == nil {
			t.Fatal("invalid feedback accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid feedback sent")
	}
	if _, err := client.Feedback(context.Background(), "token", ipc.Feedback_params{Kind: "problem", Message: "Synthetic problem"}); !Is_code(err, Code_unauthorized) {
		t.Fatalf("error = %v", err)
	}
}

func Test_report_problem_posts_the_attachments_and_never_the_draft_id(t *testing.T) {
	const draft = "0123456789abcdef0123456789abcdef"
	var raw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/feedback" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("incorrect authenticated feedback request")
		}
		raw, _ = io.ReadAll(r.Body)
		write_success(w, `{"accepted":true,"code":"R-TEST"}`)
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})

	diagnostics := json.RawMessage(`{"schema":1,"connection":{"state":"failed"}}`)
	log := "time=2026-10-27T10:00:00Z level=INFO msg=\"service starting\"\n"
	got, err := client.Report_problem(context.Background(), "synthetic-token",
		ipc.Feedback_params{Kind: "problem", Message: " It keeps dropping ", Draft: draft}, diagnostics, log)
	if err != nil || !got.Accepted || got.Code != "R-TEST" {
		t.Fatalf("receipt = %+v, %v", got, err)
	}
	if strings.Contains(string(raw), draft) || strings.Contains(string(raw), "draft") {
		t.Fatalf("the local draft id left the machine: %s", raw)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 4 {
		t.Fatalf("body fields = %d, want kind, message, diagnostics, log: %s", len(body), raw)
	}
	var kind, message, sent_log string
	if json.Unmarshal(body["kind"], &kind) != nil || kind != "problem" ||
		json.Unmarshal(body["message"], &message) != nil || message != "It keeps dropping" ||
		json.Unmarshal(body["log"], &sent_log) != nil || sent_log != log {
		t.Fatalf("unexpected body: %s", raw)
	}
	var sent_diagnostics map[string]any
	if err := json.Unmarshal(body["diagnostics"], &sent_diagnostics); err != nil || sent_diagnostics["schema"] != float64(1) {
		t.Fatalf("diagnostics went as an object? %s (%v)", body["diagnostics"], err)
	}
}

func Test_feedback_without_attachments_posts_only_kind_and_message(t *testing.T) {
	var raw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		write_success(w, `{"accepted":true,"code":"R-TEST"}`)
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	// A draft id on plain feedback is the local service's business; the
	// server hears nothing of it and no empty attachment either.
	if _, err := client.Feedback(context.Background(), "t", ipc.Feedback_params{Kind: "problem", Message: "m", Draft: "0123456789abcdef0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"kind":"problem","message":"m"}` {
		t.Fatalf("body = %s", raw)
	}
}

// The per-attempt timeout is the client's; a report with attachments is given
// more than that, and a plain receipt for a sentence is not.
func Test_report_problem_with_attachments_outwaits_the_clients_attempt_timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		write_success(w, `{"accepted":true,"code":"R-TEST"}`)
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL, Timeout: 100 * time.Millisecond})
	params := ipc.Feedback_params{Kind: "problem", Message: "m"}

	if _, err := client.Feedback(context.Background(), "t", params); err == nil {
		t.Fatal("a plain receipt waited longer than the client's attempt timeout")
	}
	got, err := client.Report_problem(context.Background(), "t", params, json.RawMessage(`{"schema":1}`), "log\n")
	if err != nil || got.Code != "R-TEST" {
		t.Fatalf("a report with attachments: %+v, %v", got, err)
	}
	if client.timeout != 100*time.Millisecond {
		t.Fatalf("the client's own timeout was changed to %v", client.timeout)
	}
}

func Test_report_problem_refuses_attachments_that_do_not_belong(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		write_success(w, `{"accepted":true,"code":"R-TEST"}`)
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	if _, err := client.Report_problem(context.Background(), "t", ipc.Feedback_params{Kind: "idea", Message: "m"}, json.RawMessage(`{}`), ""); err == nil {
		t.Fatal("an idea carried diagnostics")
	}
	if _, err := client.Report_problem(context.Background(), "t", ipc.Feedback_params{Kind: "problem", Message: "m"}, json.RawMessage(`{broken`), ""); err == nil {
		t.Fatal("diagnostics that are not JSON were sent")
	}
	if calls != 0 {
		t.Fatalf("%d requests went out", calls)
	}
}

func Test_account_spending_never_inferred_from_beneficiary_history(t *testing.T) {
	for _, payments := range []string{`null`, `[]`, `[{"id":"a1","payment_id":"p1","amount_minor":500,"payment_amount_minor":1500,"payment_currency":"RUB"},{"id":"a2","payment_id":"p1","amount_minor":500,"payment_amount_minor":1500,"payment_currency":"RUB"}]`, `[{"id":"sponsored","amount_minor":0,"payment_currency":"USD"}]`, `[{"id":"missing"}]`} {
		t.Run(payments, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/me" {
					write_success(w, account_fixture)
					return
				}
				if r.URL.Path == "/api/v1/billing/payments" {
					write_success(w, `{"payments":`+payments+`}`)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL})
			got, err := client.Account(context.Background(), "synthetic-token")
			if err != nil {
				t.Fatal(err)
			}
			if got.Spending.Status != "unavailable" || got.Spending.Totals != nil {
				t.Fatalf("unproven spending: %+v", got.Spending)
			}
		})
	}
}

func Test_account_authenticated_projection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("missing authenticated token")
		}
		switch r.URL.Path {
		case "/api/v1/me":
			write_success(w, account_fixture)
		case "/api/v1/billing/payments":
			write_success(w, `{"payments":[{"id":"p1","payment_id":"payment1","user_id":"u1","amount_minor":500,"plan":"fast","period_start":"2026-10-01T00:00:00Z","period_end":"2026-11-01T00:00:00Z","created_at":"2026-10-01T00:00:00Z","payment_amount_minor":1500,"payment_occurred_at":"2026-10-01T00:00:00Z","payment_currency":"RUB"}]}`)
		case "/api/v1/referral/list":
			write_success(w, `{"referrals":[],"sponsored_used":0,"sponsored_total":2,"sponsored_plan":"fast"}`)
		case "/api/v1/policies":
			write_success(w, `{"plans":[{"plan":"slow","rate_kbps":100,"target_kbps":200},{"plan":"fast","rate_kbps":1000,"target_kbps":5000}],"current":{"plan":"fast","rate_kbps":2500,"ceil_kbps":9000,"personal_bandwidth":true},"invites":{"tickets":0}}`)
		case "/api/v1/billing/plans":
			write_success(w, `[{"plan":"slow","amount_minor":100,"currency":"RUB","period_days":30},{"plan":"fast","amount_minor":200,"currency":"RUB","period_days":30}]`)
		case "/api/v1/me/contribution":
			write_success(w, `{"reports":[],"summary":{"reports":0,"bugs":0,"fixed":0,"score":0}}`)
		case "/api/v1/auth/devices":
			write_success(w, `{"devices":[{"device_id":"d1","name":"Desktop","token":"SECRET"}]}`)
		case "/api/v1/billing/spending":
			write_success(w, strings.Replace(spending_fixture, `"snapshot":"opaque"`, `"snapshot":"SECRET"`, 1))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	got, err := client.Account(context.Background(), "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan.Name != "Gaming" || got.Plan.Expires_at == nil || got.Credits == nil || *got.Credits != 0 || got.Referrals == nil || len(got.Devices) != 1 || got.Payments[0].Amount_minor == nil {
		t.Fatalf("unexpected account: %+v", got)
	}
	if *got.Payments[0].Amount_minor != 500 || got.Payments[0].Payment_amount_minor == nil || *got.Payments[0].Payment_amount_minor != 1500 || got.Payments[0].Payment_currency != "RUB" || got.Payments[0].Payment_occurred_at == nil {
		t.Fatalf("payment projection = %+v", got.Payments[0])
	}
	if got.Referrer == nil || got.Referrer.Username != "inviter" || got.Invite_tickets == nil || *got.Invite_tickets != 0 || got.Network_score == nil || *got.Network_score != 0 || got.Plan.Sponsored_place == nil || *got.Plan.Sponsored_place || got.Sponsored_used == nil || *got.Sponsored_used != 0 || got.Sponsored_total == nil || *got.Sponsored_total != 2 || got.Sponsored_plan == nil || *got.Sponsored_plan != "fast" {
		t.Fatalf("network projection = %+v", got)
	}
	raw, _ := json.Marshal(got)
	if got.Plan.Expires_at.Format(time.RFC3339) != "2027-01-01T00:00:00Z" {
		t.Fatal("profile expiry replaced subscription expiry")
	}
	if got.Spending.Status != "available" || len(got.Spending.Totals) != 1 || got.Spending.Totals[0].Amount_minor != 1500 {
		t.Fatalf("spending is not the payer total: %+v", got.Spending)
	}
	if got.Plan.Effective_rate_kbps == nil || *got.Plan.Effective_rate_kbps != 2500 || got.Plan.Effective_ceil_kbps == nil || *got.Plan.Effective_ceil_kbps != 9000 || got.Plan.Personal_bandwidth == nil || !*got.Plan.Personal_bandwidth {
		t.Fatalf("personal policy lost: %+v", got.Plan)
	}
	if got.Plan.Rate_kbps == nil || *got.Plan.Rate_kbps != 1000 || got.Plan.Target_kbps == nil || *got.Plan.Target_kbps != 5000 || got.Plan.Amount_minor == nil || *got.Plan.Amount_minor != 200 || got.Plan.Currency == nil || *got.Plan.Currency != "RUB" || got.Plan.Period_days == nil || *got.Plan.Period_days != 30 || len(got.Section_errors) != 0 {
		t.Fatalf("tariff policy/price = %+v, errors = %v", got.Plan, got.Section_errors)
	}
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "synthetic-token") {
		t.Fatal("secret escaped projection")
	}
}

func Test_account_missing_required_fields(t *testing.T) {
	for _, body := range []string{`null`, `{}`, strings.Replace(account_fixture, `"client_limit":3,`, "", 1), strings.Replace(account_fixture, `"active":true,`, "", 1), strings.Replace(account_fixture, `,"expires_at":"2027-01-01T00:00:00Z"`, "", 1)} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { write_success(w, body) }))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL})
			if _, err := client.Account(context.Background(), "token"); !errors.Is(err, Err_incomplete_response) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func Test_account_unknown_credit_and_optional_failures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me":
			write_success(w, `{"user_id":"u1","username":"synthetic","plan":"slow","client_limit":1,"subscription":{"active":false}}`)
		case "/api/v1/referral/list":
			write_success(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	got, err := client.Account(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if got.Credits != nil || got.Plan.Expires_at != nil || got.Payments != nil || got.Devices != nil || got.Invite_tickets != nil || got.Referrer != nil || got.Network_score != nil || got.Plan.Sponsored_place != nil || got.Sponsored_used != nil || got.Sponsored_total != nil || got.Sponsored_plan != nil || got.Plan.Rate_kbps != nil || got.Plan.Target_kbps != nil || got.Plan.Amount_minor != nil || got.Plan.Currency != nil || got.Plan.Period_days != nil || len(got.Section_errors) != 7 || got.Section_errors["referrals"] != "incomplete_response" {
		t.Fatalf("unexpected account: %+v", got)
	}
}

func Test_account_rejects_unauthorized(t *testing.T) {
	for _, unauthorized_path := range []string{"/api/v1/me", "/api/v1/auth/devices"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == unauthorized_path {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED","message":"unauthorized"}}`))
				return
			}
			if r.URL.Path == "/api/v1/me" {
				write_success(w, account_fixture)
			} else {
				write_success(w, `{"payments":[],"referrals":[],"devices":[]}`)
			}
		}))
		client, _ := New(Config{Base_url: server.URL})
		_, err := client.Account(context.Background(), "token")
		server.Close()
		if !Is_code(err, Code_unauthorized) {
			t.Fatalf("error = %v", err)
		}
	}
}

func Test_account_sections_start_in_parallel(t *testing.T) {
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			write_success(w, account_fixture)
			return
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		write_success(w, `{"payments":[],"referrals":[],"devices":[],"invites":{"tickets":2}}`)
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	done := make(chan error, 1)
	go func() { _, err := client.Account(context.Background(), "token"); done <- err }()
	all_started := true
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for range 6 {
		select {
		case <-started:
		case <-deadline.C:
			all_started = false
		}
		if !all_started {
			break
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !all_started {
		t.Fatal("optional sections did not start concurrently")
	}
}

func Test_account_sections_timeout_preserves_main_account(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			write_success(w, account_fixture)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	start := time.Now()
	got, err := client.Account(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > account_sections_timeout+2*time.Second {
		t.Fatalf("optional requests exceeded shared budget: %s", elapsed)
	}
	if got.Username != "synthetic" || len(got.Section_errors) != 7 || got.Payments != nil || got.Invite_tickets != nil {
		t.Fatalf("unexpected partial result: %+v", got)
	}
}

func Test_account_parent_cancellation_cancels_all_sections(t *testing.T) {
	started := make(chan struct{}, 6)
	stopped := make(chan struct{}, 6)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			write_success(w, account_fixture)
			return
		}
		started <- struct{}{}
		<-r.Context().Done()
		stopped <- struct{}{}
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Account(ctx, "token"); done <- err }()
	for range 6 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("sections did not start")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	for range 6 {
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Fatal("request survived caller cancellation")
		}
	}
}

func Test_account_free_sponsored_plan_and_missing_payment_amount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			write_success(w, `{"user_id":"u1","username":"synthetic","plan":"fast","client_limit":3,"subscription":{"active":false},"sponsored_place":true}`)
			return
		}
		write_success(w, `{"payments":[{"id":"grant","amount_minor":0,"plan":"fast"}],"referrals":[],"devices":[],"invites":{"tickets":1}}`)
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	got, err := client.Account(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan.Id != "fast" || got.Plan.Subscription_active || got.Plan.Expires_at != nil || got.Plan.Sponsored_place == nil || !*got.Plan.Sponsored_place {
		t.Fatalf("sponsored plan = %+v", got.Plan)
	}
	if got.Payments[0].Payment_amount_minor != nil || got.Payments[0].Amount_minor == nil || *got.Payments[0].Amount_minor != 0 {
		t.Fatalf("missing parent payment became a monetary zero: %+v", got.Payments[0])
	}
}

func Test_account_optional_tariff_values_are_never_inferred(t *testing.T) {
	for _, test := range []struct {
		name, policies, prices    string
		known                     bool
		policy_error, price_error bool
	}{
		{"known_zero", `{"plans":[{"plan":"fast","rate_kbps":0,"target_kbps":0}],"current":{"plan":"fast","rate_kbps":0,"ceil_kbps":0,"personal_bandwidth":false},"invites":{"tickets":0}}`, `[{"plan":"fast","amount_minor":0,"currency":"RUB","period_days":30}]`, true, false, false},
		{"other_plan", `{"plans":[{"plan":"slow","rate_kbps":1,"target_kbps":2}],"invites":{"tickets":0}}`, `[{"plan":"slow","amount_minor":1,"currency":"RUB","period_days":30}]`, false, true, false},
		{"missing_fields", `{"plans":[{"plan":"fast"}],"invites":{"tickets":0}}`, `[{"plan":"fast","currency":"RUB","period_days":30}]`, false, true, true},
		{"null", `null`, `null`, false, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/me":
					write_success(w, account_fixture)
				case "/api/v1/policies":
					write_success(w, test.policies)
				case "/api/v1/billing/plans":
					write_success(w, test.prices)
				default:
					write_success(w, `{"payments":[],"referrals":[],"devices":[]}`)
				}
			}))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL})
			got, err := client.Account(context.Background(), "token")
			if err != nil {
				t.Fatal(err)
			}
			if test.known {
				if got.Plan.Effective_rate_kbps == nil || *got.Plan.Effective_rate_kbps != 0 || got.Plan.Effective_ceil_kbps == nil || *got.Plan.Effective_ceil_kbps != 0 || got.Plan.Personal_bandwidth == nil || *got.Plan.Personal_bandwidth {
					t.Fatalf("known effective policy lost: %+v", got.Plan)
				}
				if got.Plan.Rate_kbps == nil || *got.Plan.Rate_kbps != 0 || got.Plan.Target_kbps == nil || *got.Plan.Target_kbps != 0 || got.Plan.Amount_minor == nil || *got.Plan.Amount_minor != 0 {
					t.Fatalf("known zero lost: %+v", got.Plan)
				}
			} else if got.Plan.Rate_kbps != nil || got.Plan.Target_kbps != nil || got.Plan.Amount_minor != nil || got.Plan.Currency != nil || got.Plan.Period_days != nil {
				t.Fatalf("unknown values inferred: %+v", got.Plan)
			}
			if (got.Section_errors["policies"] != "") != test.policy_error || (got.Section_errors["prices"] != "") != test.price_error {
				t.Fatalf("section errors = %v", got.Section_errors)
			}
		})
	}
}

func Test_account_payment_null_is_empty_but_missing_is_incomplete(t *testing.T) {
	for _, test := range []struct {
		body  string
		empty bool
	}{
		{`{"payments":null}`, true}, {`{"payments":[]}`, true}, {`{}`, false}, {`null`, false},
	} {
		t.Run(test.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/me" {
					write_success(w, account_fixture)
					return
				}
				if r.URL.Path == "/api/v1/billing/payments" {
					write_success(w, test.body)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL})
			got, err := client.Account(context.Background(), "token")
			if err != nil {
				t.Fatal(err)
			}
			if test.empty {
				if got.Payments == nil || len(got.Payments) != 0 || got.Section_errors["payments"] != "" {
					t.Fatalf("present empty payments = %+v, errors = %v", got.Payments, got.Section_errors)
				}
			} else if got.Payments != nil || got.Section_errors["payments"] != "incomplete_response" {
				t.Fatalf("missing payments = %+v, errors = %v", got.Payments, got.Section_errors)
			}
		})
	}
}

func Test_account_effective_policy_is_not_inferred_from_tariff(t *testing.T) {
	for _, current := range []string{`null`, `{"plan":"slow","rate_kbps":8000,"ceil_kbps":9000,"personal_bandwidth":true}`, `{"plan":"fast"}`} {
		t.Run(current, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/me" {
					write_success(w, account_fixture)
					return
				}
				if r.URL.Path == "/api/v1/policies" {
					write_success(w, `{"current":`+current+`,"plans":[{"plan":"fast","rate_kbps":1000,"target_kbps":5000}],"invites":{"tickets":1}}`)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL})
			got, err := client.Account(context.Background(), "token")
			if err != nil {
				t.Fatal(err)
			}
			if got.Plan.Effective_rate_kbps != nil || got.Plan.Effective_ceil_kbps != nil || got.Plan.Personal_bandwidth != nil || got.Plan.Rate_kbps == nil || *got.Plan.Rate_kbps != 1000 || got.Section_errors["policies"] != "incomplete_response" {
				t.Fatalf("effective policy inferred: %+v", got.Plan)
			}
		})
	}
}

func Test_account_contribution_projection(t *testing.T) {
	for _, test := range []struct {
		name, body  string
		unavailable bool
	}{
		{"reports", `{"reports":[{"code":"BUG-1","created_at":"2026-10-25T10:00:00Z","kind":"bug","message":"Synthetic report","verdict":"confirmed","score":0,"score_source":"human","score_reason":"Reviewed","fixed_commit":"abc123","context":"SECRET","app_version":"PRIVATE"},{"code":"IDEA-2","kind":"idea","message":"Synthetic idea","score":null,"score_source":"ai"}],"summary":{"reports":2,"bugs":1,"fixed":1,"score":0}}`, false},
		{"empty", `{"reports":[],"summary":{"reports":0,"bugs":0,"fixed":0,"score":0}}`, false},
		{"missing_reports", `{"summary":{"reports":0}}`, false},
		{"missing_summary", `{"reports":[]}`, false},
		{"malformed", `{"reports":[{"code":"BUG-1","score":"unknown"}],"summary":{}}`, false},
		{"unavailable", ``, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("missing authenticated token")
				}
				switch r.URL.Path {
				case "/api/v1/me":
					write_success(w, account_fixture)
				case "/api/v1/me/contribution":
					if test.unavailable {
						http.NotFound(w, r)
					} else {
						write_success(w, test.body)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL})
			got, err := client.Account(context.Background(), "synthetic-token")
			if err != nil {
				t.Fatal(err)
			}
			switch test.name {
			case "reports":
				if len(got.Reports) != 2 || got.Contribution_summary == nil || got.Contribution_summary.Reports == nil || *got.Contribution_summary.Reports != 2 || got.Contribution_summary.Score == nil || *got.Contribution_summary.Score != 0 {
					t.Fatalf("contribution = %+v, %+v", got.Reports, got.Contribution_summary)
				}
				report := got.Reports[0]
				if report.Code != "BUG-1" || report.Created_at == nil || report.Kind != "bug" || report.Message != "Synthetic report" || report.Verdict != "confirmed" || report.Score == nil || *report.Score != 0 || report.Score_source != "human" || report.Score_reason != "Reviewed" || report.Fixed_commit != "abc123" || got.Reports[1].Score != nil || got.Reports[1].Score_source != "ai" {
					t.Fatalf("report = %+v", report)
				}
				raw, _ := json.Marshal(got)
				if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "PRIVATE") {
					t.Fatal("diagnostic fields escaped contribution projection")
				}
			case "empty":
				if got.Reports == nil || len(got.Reports) != 0 || got.Contribution_summary == nil || got.Contribution_summary.Reports == nil || *got.Contribution_summary.Reports != 0 {
					t.Fatal("empty contribution was not preserved")
				}
			default:
				if got.Reports != nil || got.Contribution_summary != nil || got.Section_errors["contribution"] == "" {
					t.Fatal("failed contribution appeared available")
				}
			}
		})
	}
}
