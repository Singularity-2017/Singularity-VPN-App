package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"singularity-vpn-app/internal/ipc"
)

const spending_fixture = `{"spending":{"status":"available","reason":null,"basis":"recorded_paid_payments","totals":[{"currency":"RUB","amount_minor":1500}],"complete":true,"as_of":"2026-10-25T12:00:00Z"},"transactions":[{"payment_id":"synthetic-payment","amount_minor":1500,"currency":"RUB","status":"paid","occurred_at":"2026-10-01T12:00:00Z"}],"transactions_coverage":"all_recorded_paid_payments","pagination":{"limit":50,"next_cursor":"next-synthetic","has_more":true,"snapshot":"opaque"}}`

const spending_verified_only = `{"spending":{"status":"unavailable","reason":"incomplete_payment_history","basis":"recorded_paid_payments","totals":null,"complete":false,"as_of":"2026-10-25T12:00:00Z"},"transactions":[{"payment_id":"synthetic-payment","amount_minor":1500,"currency":"RUB","status":"paid","occurred_at":"2026-10-01T12:00:00Z"}],"transactions_coverage":"verified_paid_payments_only","pagination":{"limit":50,"next_cursor":null,"has_more":false,"snapshot":"opaque"}}`

func account_with_spending(t *testing.T, spending string) ipc.Account_data {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me":
			write_success(w, account_fixture)
		case spending_path:
			if r.URL.RawQuery != "" {
				t.Errorf("first page sent a query: %s", r.URL.RawQuery)
			}
			write_success(w, spending)
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
	return got
}

func Test_account_spending_projects_the_contract(t *testing.T) {
	got := account_with_spending(t, spending_fixture)
	s := got.Spending
	if _, failed := got.Section_errors["spending"]; failed || s.Status != "available" || len(s.Totals) != 1 || s.Totals[0] != (ipc.Account_currency_total{Currency: "RUB", Amount_minor: 1500}) {
		t.Fatalf("spending = %+v, errors = %v", s, got.Section_errors)
	}
	if s.Coverage != "all_recorded_paid_payments" || s.Next_cursor != "next-synthetic" || s.Restarted || len(s.Transactions) != 1 {
		t.Fatalf("page = %+v", s)
	}
	if tr := s.Transactions[0]; tr.Payment_id != "synthetic-payment" || tr.Amount_minor != 1500 || tr.Currency != "RUB" || tr.Occurred_at.IsZero() {
		t.Fatalf("transaction = %+v", tr)
	}
}

func Test_account_spending_verified_subset_carries_no_total(t *testing.T) {
	s := account_with_spending(t, spending_verified_only).Spending
	if s.Status != "unavailable" || s.Reason != "incomplete_payment_history" || s.Totals != nil || s.Coverage != "verified_paid_payments_only" || len(s.Transactions) != 1 || s.Next_cursor != "" {
		t.Fatalf("spending = %+v", s)
	}
}

func Test_account_spending_empty_history_is_not_a_zero_total(t *testing.T) {
	body := strings.Replace(spending_fixture, `[{"currency":"RUB","amount_minor":1500}]`, `[]`, 1)
	body = strings.Replace(body, `[{"payment_id":"synthetic-payment","amount_minor":1500,"currency":"RUB","status":"paid","occurred_at":"2026-10-01T12:00:00Z"}]`, `[]`, 1)
	body = strings.Replace(body, `"next_cursor":"next-synthetic","has_more":true`, `"next_cursor":null,"has_more":false`, 1)
	s := account_with_spending(t, body).Spending
	raw, _ := json.Marshal(s)
	if s.Status != "available" || s.Totals == nil || len(s.Totals) != 0 || !strings.Contains(string(raw), `"totals":[]`) || !strings.Contains(string(raw), `"transactions":[]`) {
		t.Fatalf("empty history = %s", raw)
	}
}

func Test_account_spending_unavailable_never_carries_a_total(t *testing.T) {
	body := strings.Replace(spending_verified_only, `"totals":null,"complete":false`, `"totals":[{"currency":"RUB","amount_minor":1500}],"complete":true`, 1)
	got := account_with_spending(t, body)
	if _, failed := got.Section_errors["spending"]; failed || got.Spending.Status != "unavailable" || got.Spending.Totals != nil || len(got.Spending.Transactions) != 1 {
		t.Fatalf("spending = %+v, errors = %v", got.Spending, got.Section_errors)
	}
}

func Test_account_spending_outside_the_contract_is_unknown(t *testing.T) {
	mutations := []struct{ name, base, old, new string }{
		{"other basis", spending_fixture, `"recorded_paid_payments"`, `"wallet_balance"`},
		{"available but incomplete", spending_fixture, `"complete":true`, `"complete":false`},
		{"available without complete", spending_fixture, `,"complete":true`, ``},
		{"available without totals", spending_fixture, `"totals":[{"currency":"RUB","amount_minor":1500}]`, `"totals":null`},
		{"unknown status", spending_fixture, `"status":"available"`, `"status":"partial"`},
		{"negative total", spending_fixture, `"amount_minor":1500}]`, `"amount_minor":-1500}]`},
		{"total past safe integer", spending_fixture, `"amount_minor":1500}]`, `"amount_minor":9007199254740992}]`},
		{"lowercase currency", spending_fixture, `"currency":"RUB","amount_minor":1500}`, `"currency":"rub","amount_minor":1500}`},
		{"duplicate currency", spending_fixture, `[{"currency":"RUB","amount_minor":1500}]`, `[{"currency":"RUB","amount_minor":1500},{"currency":"RUB","amount_minor":1}]`},
		{"payments under an empty total", spending_fixture, `[{"currency":"RUB","amount_minor":1500}]`, `[]`},
		{"payment outside the totals", spending_fixture, `"currency":"RUB","status"`, `"currency":"EUR","status"`},
		{"malformed payment currency", spending_fixture, `"currency":"RUB","status"`, `"currency":"RU","status"`},
		{"available, partial list", spending_fixture, `"all_recorded_paid_payments"`, `"verified_paid_payments_only"`},
		{"refunded transaction", spending_fixture, `"status":"paid"`, `"status":"refunded"`},
		{"zero transaction", spending_fixture, `"amount_minor":1500,"currency":"RUB","status"`, `"amount_minor":0,"currency":"RUB","status"`},
		{"undated transaction", spending_fixture, `,"occurred_at":"2026-10-01T12:00:00Z"`, ``},
		{"no transaction list", spending_fixture, `"transactions":[`, `"transactions":null,"x":[`},
		{"no pagination", spending_fixture, `,"pagination":{"limit":50,"next_cursor":"next-synthetic","has_more":true,"snapshot":"opaque"}`, ``},
		{"no has_more", spending_fixture, `,"has_more":true`, ``},
		{"more without cursor", spending_fixture, `"next_cursor":"next-synthetic"`, `"next_cursor":null`},
		{"cursor without more", spending_fixture, `"has_more":true`, `"has_more":false`},
		{"unavailable without reason", spending_verified_only, `"reason":"incomplete_payment_history"`, `"reason":null`},
		{"unavailable, empty reason", spending_verified_only, `"reason":"incomplete_payment_history"`, `"reason":""`},
		{"unavailable, complete list", spending_verified_only, `"verified_paid_payments_only"`, `"all_recorded_paid_payments"`},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			body := strings.Replace(mutation.base, mutation.old, mutation.new, 1)
			if body == mutation.base {
				t.Fatal("mutation did not apply")
			}
			got := account_with_spending(t, body)
			if got.Section_errors["spending"] != "incomplete_response" || got.Spending.Status != "unavailable" || got.Spending.Totals != nil || got.Spending.Transactions != nil {
				t.Fatalf("spending = %+v, errors = %v", got.Spending, got.Section_errors)
			}
		})
	}
}

func Test_account_without_spending_endpoint_reads_the_rest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			write_success(w, account_fixture)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	got, err := client.Account(context.Background(), "synthetic-token")
	if err != nil || got.Username != "synthetic" {
		t.Fatalf("account = %+v, %v", got, err)
	}
	if got.Section_errors["spending"] != "unavailable" || got.Spending.Status != "unavailable" || got.Spending.Totals != nil {
		t.Fatalf("spending = %+v, errors = %v", got.Spending, got.Section_errors)
	}
}

func Test_spending_page_continues_from_the_cursor(t *testing.T) {
	last := strings.Replace(spending_fixture, `"next_cursor":"next-synthetic","has_more":true`, `"next_cursor":null,"has_more":false`, 1)
	const cursor = "a+b&limit=1=%2F%"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if query := r.URL.Query(); r.URL.Path != spending_path || r.Header.Get("Authorization") != "Bearer synthetic-token" || len(query) != 1 || len(query["cursor"]) != 1 || query.Get("cursor") != cursor {
			t.Errorf("unexpected request: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		write_success(w, last)
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	got, err := client.Spending_page(context.Background(), "synthetic-token", cursor)
	if err != nil || got.Restarted || got.Next_cursor != "" || len(got.Transactions) != 1 || len(got.Totals) != 1 {
		t.Fatalf("page = %+v, %v", got, err)
	}
}

func Test_spending_page_restarts_a_void_traversal(t *testing.T) {
	for _, refusal := range []struct {
		status int
		code   string
	}{{http.StatusConflict, Code_snapshot_changed}, {http.StatusBadRequest, Code_invalid_input}} {
		t.Run(refusal.code, func(t *testing.T) {
			var queries []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				queries = append(queries, r.URL.RawQuery)
				if r.URL.RawQuery != "" {
					w.WriteHeader(refusal.status)
					w.Write([]byte(`{"success":false,"error":{"code":"` + refusal.code + `","message":"synthetic"}}`))
					return
				}
				write_success(w, spending_fixture)
			}))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL})
			got, err := client.Spending_page(context.Background(), "synthetic-token", "stale")
			if err != nil || !got.Restarted || got.Next_cursor != "next-synthetic" || len(got.Transactions) != 1 {
				t.Fatalf("page = %+v, %v", got, err)
			}
			if len(queries) != 2 || queries[0] != "cursor=stale" || queries[1] != "" {
				t.Fatalf("queries = %q", queries)
			}
		})
	}
}

func Test_spending_page_restarts_once(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"success":false,"error":{"code":"SNAPSHOT_CHANGED","message":"synthetic"}}`))
	}))
	defer server.Close()
	client, _ := New(Config{Base_url: server.URL})
	if _, err := client.Spending_page(context.Background(), "synthetic-token", "stale"); !Is_code(err, Code_snapshot_changed) || calls != 2 {
		t.Fatalf("error = %v after %d calls", err, calls)
	}
}

func Test_spending_page_other_failures_stay_errors(t *testing.T) {
	for _, refusal := range []struct {
		status int
		code   string
	}{{http.StatusInternalServerError, Code_internal_error}, {http.StatusUnauthorized, Code_unauthorized}} {
		t.Run(refusal.code, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(refusal.status)
				w.Write([]byte(`{"success":false,"error":{"code":"` + refusal.code + `","message":"synthetic"}}`))
			}))
			defer server.Close()
			client, _ := New(Config{Base_url: server.URL})
			if _, err := client.Spending_page(context.Background(), "synthetic-token", "c1"); !Is_code(err, refusal.code) || calls != 1 {
				t.Fatalf("error = %v after %d calls", err, calls)
			}
		})
	}
}
