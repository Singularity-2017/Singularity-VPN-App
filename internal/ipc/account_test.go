package ipc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type account_test_service struct{ *fake_service }

func (s *account_test_service) Feedback(_ context.Context, params Feedback_params) (Feedback_result, error) {
	return Feedback_result{Accepted: true, Code: "R-TEST"}, nil
}

func Test_feedback_pipe_validation_and_round_trip(t *testing.T) {
	client, _ := serve_pair(t, &account_test_service{new_fake_service()})
	got, err := client.Feedback(context.Background(), Feedback_params{Kind: "problem", Message: "Synthetic problem"})
	if err != nil || !got.Accepted || got.Code != "R-TEST" {
		t.Fatalf("feedback = %+v, %v", got, err)
	}
	for _, params := range []Feedback_params{{Kind: "bug", Message: "test"}, {Kind: "idea", Message: " "}, {Kind: "idea", Message: strings.Repeat("a", 2001)}} {
		// Bypass the client validator to prove the service boundary validates too.
		resp, err := client.call(context.Background(), Command_feedback, params)
		if err != nil {
			t.Fatal(err)
		}
		var result Feedback_result
		if decode_result(resp, &result) == nil {
			t.Fatal("invalid IPC feedback accepted")
		}
	}
	unsupported, _ := serve_pair(t, new_fake_service())
	if _, err := unsupported.Feedback(context.Background(), Feedback_params{Kind: "idea", Message: "Synthetic idea"}); err == nil {
		t.Fatal("unsupported feedback accepted")
	}
}

func (s *account_test_service) Spending_page(_ context.Context, params Spending_page_params) (Account_spending, error) {
	return Account_spending{Status: "available", Totals: []Account_currency_total{}, Transactions: []Account_transaction{{Payment_id: params.Cursor, Amount_minor: 1500, Currency: "RUB"}}, Restarted: true}, nil
}

func Test_spending_page_pipe_validation_and_round_trip(t *testing.T) {
	client, _ := serve_pair(t, &account_test_service{new_fake_service()})
	got, err := client.Spending_page(context.Background(), Spending_page_params{Cursor: "c1"})
	if err != nil || !got.Restarted || len(got.Transactions) != 1 || got.Transactions[0].Payment_id != "c1" || got.Totals == nil {
		t.Fatalf("page = %+v, %v", got, err)
	}
	for _, params := range []Spending_page_params{{}, {Cursor: strings.Repeat("a", 4097)}} {
		// Bypass the client validator to prove the service boundary validates too.
		resp, err := client.call(context.Background(), Command_spending_page, params)
		if err != nil {
			t.Fatal(err)
		}
		var result Account_spending
		if decode_result(resp, &result) == nil {
			t.Fatal("invalid spending cursor accepted")
		}
	}
	unsupported, _ := serve_pair(t, new_fake_service())
	if _, err := unsupported.Spending_page(context.Background(), Spending_page_params{Cursor: "c1"}); err == nil {
		t.Fatal("unsupported spending page served")
	}
}

func Test_account_unknown_spending_contract(t *testing.T) {
	raw, err := json.Marshal(Account_data{Spending: Account_spending{Status: "unavailable", Reason: "payer_history_not_exposed"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"totals":null`) {
		t.Fatal("unknown spending became a number")
	}
}

func (s *account_test_service) Account(context.Context) (Account_data, error) {
	zero := float64(0)
	tickets := 0
	amount := int64(1500)
	sponsored := true
	result := Account_data{Username: "synthetic", Credits: &zero, Invite_tickets: &tickets, Referrer: &Account_referrer{Username: "inviter"}, Network_score: &tickets, Sponsored_used: &tickets, Sponsored_total: &tickets, Plan: Account_plan{Id: "fast", Name: "Gaming", Sponsored_place: &sponsored, Rate_kbps: &amount, Target_kbps: &amount}, Payments: []Account_payment{{Id: "p1", Payment_amount_minor: &amount}}, Devices: []Account_device{{Device_id: "d1", Name: "Desktop"}}}
	result.Plan.Effective_rate_kbps, result.Plan.Effective_ceil_kbps, result.Plan.Personal_bandwidth = &amount, &amount, &sponsored
	result.Reports = []Account_report{{Code: "BUG-1", Score: &tickets, Score_source: "human", Score_reason: "Reviewed"}}
	result.Contribution_summary = &Account_contribution_summary{Score: &tickets}
	return result, nil
}

func Test_account_pipe_round_trip(t *testing.T) {
	client, _ := serve_pair(t, &account_test_service{new_fake_service()})
	got, err := client.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "synthetic" || got.Credits == nil || *got.Credits != 0 || got.Plan.Name != "Gaming" || len(got.Devices) != 1 {
		t.Fatalf("unexpected account: %+v", got)
	}
	if got.Invite_tickets == nil || *got.Invite_tickets != 0 || got.Referrer == nil || got.Referrer.Username != "inviter" || got.Network_score == nil || got.Sponsored_used == nil || got.Sponsored_total == nil || got.Plan.Sponsored_place == nil || !*got.Plan.Sponsored_place || got.Plan.Rate_kbps == nil || got.Plan.Target_kbps == nil || len(got.Payments) != 1 || got.Payments[0].Payment_amount_minor == nil || *got.Payments[0].Payment_amount_minor != 1500 {
		t.Fatalf("account display fields lost in IPC: %+v", got)
	}
	if got.Plan.Effective_rate_kbps == nil || *got.Plan.Effective_rate_kbps != 1500 || got.Plan.Effective_ceil_kbps == nil || *got.Plan.Effective_ceil_kbps != 1500 || got.Plan.Personal_bandwidth == nil || !*got.Plan.Personal_bandwidth || len(got.Reports) != 1 || got.Reports[0].Score_source != "human" || got.Reports[0].Score_reason != "Reviewed" || got.Reports[0].Score == nil || *got.Reports[0].Score != 0 || got.Contribution_summary == nil || got.Contribution_summary.Score == nil || *got.Contribution_summary.Score != 0 {
		t.Fatalf("effective policy or contribution lost in IPC: %+v", got)
	}
}

func Test_account_unsupported_service(t *testing.T) {
	client, _ := serve_pair(t, new_fake_service())
	if _, err := client.Account(context.Background()); err == nil {
		t.Fatal("unsupported account call succeeded")
	}
}
