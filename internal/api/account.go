package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"singularity-vpn-app/internal/ipc"
)

// Leave room for IPC delivery under a 15-second caller deadline.
const account_timeout = 12 * time.Second
const account_sections_timeout = 4 * time.Second

type account_payments_response struct {
	Payments []ipc.Account_payment
	present  bool
}

func (p *account_payments_response) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Payments json.RawMessage `json:"payments"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	p.present = len(wire.Payments) != 0
	if !p.present {
		return nil
	}
	if err := json.Unmarshal(wire.Payments, &p.Payments); err != nil {
		return err
	}
	// The payment store returns nil for an empty allocation list.
	if p.Payments == nil {
		p.Payments = []ipc.Account_payment{}
	}
	return nil
}

// Account reads authenticated panel endpoints and projects an explicit display allowlist.
// Numeric pointers preserve the distinction between a known zero and missing data.
func (c *Client) Account(ctx context.Context, token string) (ipc.Account_data, error) {
	ctx, cancel := context.WithTimeout(ctx, account_timeout)
	defer cancel()
	var me struct {
		User_id         string                `json:"user_id"`
		Username        string                `json:"username"`
		Plan            string                `json:"plan"`
		Wallet          *float64              `json:"wallet"`
		Client_limit    *int                  `json:"client_limit"`
		Referral_code   string                `json:"referral_code"`
		Referrer        *ipc.Account_referrer `json:"referrer"`
		Network_score   *int                  `json:"network_score"`
		Sponsored_place *bool                 `json:"sponsored_place"`
		Subscription    *struct {
			Active     *bool      `json:"active"`
			Expires_at *time.Time `json:"expires_at"`
		} `json:"subscription"`
	}
	var result ipc.Account_data
	if err := c.do(ctx, "GET", "/api/v1/me", nil, token, nil, &me, false); err != nil {
		return result, err
	}
	if err := check_required("account", required_field{"user_id", me.User_id != ""}, required_field{"username", me.Username != ""}, required_field{"plan", me.Plan != ""}, required_field{"client_limit", me.Client_limit != nil}, required_field{"subscription.active", me.Subscription != nil && me.Subscription.Active != nil}); err != nil {
		return result, err
	}
	if *me.Subscription.Active && (me.Subscription.Expires_at == nil || me.Subscription.Expires_at.IsZero()) {
		return result, fmt.Errorf("%w: account subscription.expires_at", Err_incomplete_response)
	}
	name := map[string]string{"slow": "Office", "fast": "Gaming", "unlimited": "Gaming Ultra"}[me.Plan]
	if name == "" {
		name = me.Plan
	}
	result = ipc.Account_data{User_id: me.User_id, Username: me.Username, Credits: me.Wallet, Referral_code: me.Referral_code, Plan: ipc.Account_plan{Id: me.Plan, Name: name, Client_limit: me.Client_limit, Subscription_active: *me.Subscription.Active, Expires_at: me.Subscription.Expires_at}, Section_errors: map[string]string{}}
	result.Referrer, result.Network_score, result.Plan.Sponsored_place = me.Referrer, me.Network_score, me.Sponsored_place
	// Spending comes only from /billing/spending. /billing/payments lists
	// beneficiary allocations: parent amounts repeat and the payer may be
	// someone else, so nothing there is ever summed into spending.
	result.Spending = ipc.Account_spending{Status: "unavailable", Reason: "not_received"}
	var spending spending_response
	var payments account_payments_response
	var referrals struct {
		Referrals       []ipc.Account_referral `json:"referrals"`
		Sponsored_used  *int                   `json:"sponsored_used"`
		Sponsored_total *int                   `json:"sponsored_total"`
		Sponsored_plan  *string                `json:"sponsored_plan"`
	}
	var devices struct {
		Devices []ipc.Account_device `json:"devices"`
	}
	var contribution ipc.Account_contribution
	var policies struct {
		Current *struct {
			Plan               string `json:"plan"`
			Rate_kbps          *int64 `json:"rate_kbps"`
			Ceil_kbps          *int64 `json:"ceil_kbps"`
			Personal_bandwidth *bool  `json:"personal_bandwidth"`
		} `json:"current"`
		Plans []struct {
			Plan        string `json:"plan"`
			Rate_kbps   *int64 `json:"rate_kbps"`
			Target_kbps *int64 `json:"target_kbps"`
		} `json:"plans"`
		Invites *struct {
			Tickets *int `json:"tickets"`
		} `json:"invites"`
	}
	var prices []struct {
		Plan         string  `json:"plan"`
		Amount_minor *int64  `json:"amount_minor"`
		Currency     *string `json:"currency"`
		Period_days  *int    `json:"period_days"`
	}
	sections := []struct {
		name, path string
		out        any
		present    func() bool
	}{
		{"spending", spending_path, &spending, func() bool { return spending.Spending != nil }},
		{"payments", "/api/v1/billing/payments", &payments, func() bool { return payments.present }},
		{"referrals", "/api/v1/referral/list", &referrals, func() bool { return referrals.Referrals != nil }},
		{"devices", "/api/v1/auth/devices", &devices, func() bool { return devices.Devices != nil }},
		{"policies", "/api/v1/policies", &policies, func() bool { return policies.Invites != nil || policies.Plans != nil || policies.Current != nil }},
		{"prices", "/api/v1/billing/plans", &prices, func() bool { return prices != nil }},
		{"contribution", "/api/v1/me/contribution", &contribution, func() bool { return contribution.Reports != nil && contribution.Summary != nil }},
	}
	sections_ctx, cancel_sections := context.WithTimeout(ctx, account_sections_timeout)
	defer cancel_sections()
	type section_answer struct {
		name    string
		err     error
		present bool
	}
	answers := make(chan section_answer, len(sections))
	for _, section := range sections {
		go func() {
			err := c.do(sections_ctx, "GET", section.path, nil, token, nil, section.out, false)
			answers <- section_answer{section.name, err, section.present()}
		}()
	}
	var auth_error error
	for range sections {
		answer := <-answers
		err := answer.err
		var api_error *Error
		if Is_code(err, Code_unauthorized) || (errors.As(err, &api_error) && api_error.Status == 401) {
			auth_error = err
			cancel_sections()
		}
		if err != nil {
			result.Section_errors[answer.name] = "unavailable"
			continue
		}
		if !answer.present {
			result.Section_errors[answer.name] = "incomplete_response"
		}
	}
	if auth_error != nil {
		return ipc.Account_data{}, auth_error
	}
	if ctx.Err() != nil {
		return ipc.Account_data{}, ctx.Err()
	}
	// Decoders may fill a prefix before failing; never expose partial sections.
	if _, failed := result.Section_errors["spending"]; !failed {
		if projected, err := spending.project(); err != nil {
			result.Section_errors["spending"] = "incomplete_response"
		} else {
			result.Spending = projected
		}
	}
	if _, failed := result.Section_errors["payments"]; failed {
		payments.Payments = nil
	}
	if _, failed := result.Section_errors["referrals"]; failed {
		referrals.Referrals = nil
	} else {
		result.Sponsored_used, result.Sponsored_total, result.Sponsored_plan = referrals.Sponsored_used, referrals.Sponsored_total, referrals.Sponsored_plan
	}
	if _, failed := result.Section_errors["policies"]; !failed {
		if current := policies.Current; current != nil && current.Plan == me.Plan {
			result.Plan.Effective_rate_kbps, result.Plan.Effective_ceil_kbps, result.Plan.Personal_bandwidth = current.Rate_kbps, current.Ceil_kbps, current.Personal_bandwidth
			if current.Rate_kbps == nil || current.Ceil_kbps == nil || current.Personal_bandwidth == nil {
				result.Section_errors["policies"] = "incomplete_response"
			}
		} else {
			result.Section_errors["policies"] = "incomplete_response"
		}
		if policies.Invites != nil && policies.Invites.Tickets != nil {
			result.Invite_tickets = policies.Invites.Tickets
		} else {
			result.Section_errors["policies"] = "incomplete_response"
		}
		matched := false
		for _, row := range policies.Plans {
			if row.Plan != me.Plan {
				continue
			}
			matched = true
			result.Plan.Rate_kbps, result.Plan.Target_kbps = row.Rate_kbps, row.Target_kbps
			if row.Rate_kbps == nil || row.Target_kbps == nil {
				result.Section_errors["policies"] = "incomplete_response"
			}
			break
		}
		if !matched {
			result.Section_errors["policies"] = "incomplete_response"
		}
	}
	if _, failed := result.Section_errors["prices"]; !failed {
		for _, price := range prices {
			if price.Plan != me.Plan {
				continue
			}
			if price.Amount_minor == nil || price.Currency == nil || *price.Currency == "" || price.Period_days == nil || *price.Period_days <= 0 {
				result.Section_errors["prices"] = "incomplete_response"
				break
			}
			result.Plan.Amount_minor, result.Plan.Currency, result.Plan.Period_days = price.Amount_minor, price.Currency, price.Period_days
			break
		}
	}
	if _, failed := result.Section_errors["devices"]; failed {
		devices.Devices = nil
	}
	result.Payments, result.Referrals, result.Devices = payments.Payments, referrals.Referrals, devices.Devices
	if _, failed := result.Section_errors["contribution"]; !failed {
		result.Reports, result.Contribution_summary = contribution.Reports, contribution.Summary
	}
	return result, nil
}

const spending_path = "/api/v1/billing/spending"

// Number.MAX_SAFE_INTEGER: the contract's ceiling for any amount or total.
const max_amount_minor = 1<<53 - 1

type spending_response struct {
	Spending *struct {
		Status   string                       `json:"status"`
		Reason   *string                      `json:"reason"`
		Basis    string                       `json:"basis"`
		Totals   []ipc.Account_currency_total `json:"totals"`
		Complete *bool                        `json:"complete"`
	} `json:"spending"`
	Transactions []struct {
		Payment_id   string     `json:"payment_id"`
		Amount_minor *int64     `json:"amount_minor"`
		Currency     string     `json:"currency"`
		Status       string     `json:"status"`
		Occurred_at  *time.Time `json:"occurred_at"`
	} `json:"transactions"`
	Coverage   string `json:"transactions_coverage"`
	Pagination *struct {
		Next_cursor *string `json:"next_cursor"`
		Has_more    *bool   `json:"has_more"`
	} `json:"pagination"`
}

// project checks a page against the spending contract. Anything outside it
// makes the whole page unknown rather than a smaller or different number.
func (r *spending_response) project() (ipc.Account_spending, error) {
	bad := func(what string) (ipc.Account_spending, error) {
		return ipc.Account_spending{}, fmt.Errorf("%w: spending %s", Err_incomplete_response, what)
	}
	s := r.Spending
	if s == nil || s.Basis != "recorded_paid_payments" {
		return bad("basis")
	}
	if r.Transactions == nil || r.Pagination == nil || r.Pagination.Has_more == nil {
		return bad("page")
	}
	result := ipc.Account_spending{Status: s.Status, Coverage: r.Coverage, Transactions: make([]ipc.Account_transaction, 0, len(r.Transactions))}
	seen := map[string]bool{}
	switch {
	case s.Status == "available" && s.Complete != nil && *s.Complete && s.Totals != nil && r.Coverage == "all_recorded_paid_payments":
		for _, total := range s.Totals {
			if !valid_money(total.Amount_minor, total.Currency) || seen[total.Currency] {
				return bad("total")
			}
			seen[total.Currency] = true
		}
		result.Totals = append([]ipc.Account_currency_total{}, s.Totals...)
	case s.Status == "unavailable" && s.Reason != nil && *s.Reason != "" && r.Coverage == "verified_paid_payments_only":
		result.Reason = *s.Reason
	default:
		return bad("status")
	}
	for _, t := range r.Transactions {
		if t.Payment_id == "" || t.Amount_minor == nil || !valid_money(*t.Amount_minor, t.Currency) || t.Status != "paid" || t.Occurred_at == nil || t.Occurred_at.IsZero() {
			return bad("transaction")
		}
		// A complete total covers every listed payment's currency, or it is not complete.
		if result.Totals != nil && !seen[t.Currency] {
			return bad("transaction outside the totals")
		}
		result.Transactions = append(result.Transactions, ipc.Account_transaction{Payment_id: t.Payment_id, Amount_minor: *t.Amount_minor, Currency: t.Currency, Occurred_at: *t.Occurred_at})
	}
	next := r.Pagination.Next_cursor
	if *r.Pagination.Has_more != (next != nil && *next != "") {
		return bad("pagination")
	}
	if next != nil {
		result.Next_cursor = *next
	}
	return result, nil
}

func valid_money(amount_minor int64, currency string) bool {
	if amount_minor <= 0 || amount_minor > max_amount_minor || len(currency) != 3 {
		return false
	}
	for _, letter := range currency {
		if letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return true
}

// Spending_page continues a traversal from cursor. A history that changed
// under the snapshot (409) or a cursor the control plane no longer accepts
// (400: older than 15 minutes, or sealed with a rotated key) voids the pages
// already read, so the traversal starts over and the page says it did.
func (c *Client) Spending_page(ctx context.Context, token, cursor string) (ipc.Account_spending, error) {
	ctx, cancel := context.WithTimeout(ctx, account_timeout)
	defer cancel()
	var page spending_response
	err := c.do(ctx, "GET", spending_path, url.Values{"cursor": {cursor}}, token, nil, &page, false)
	restarted := Is_code(err, Code_snapshot_changed) || Is_code(err, Code_invalid_input)
	if restarted {
		page = spending_response{}
		err = c.do(ctx, "GET", spending_path, nil, token, nil, &page, false)
	}
	if err != nil {
		return ipc.Account_spending{}, err
	}
	result, err := page.project()
	if err != nil {
		return ipc.Account_spending{}, err
	}
	result.Restarted = restarted
	return result, nil
}

func (c *Client) Feedback(ctx context.Context, token string, params ipc.Feedback_params) (ipc.Feedback_result, error) {
	var wire struct {
		Accepted *bool  `json:"accepted"`
		Code     string `json:"code"`
	}
	if err := params.Validate(); err != nil {
		return ipc.Feedback_result{}, err
	}
	params.Message = strings.TrimSpace(params.Message)
	ctx, cancel := context.WithTimeout(ctx, account_timeout)
	defer cancel()
	if err := c.do(ctx, "POST", "/api/v1/feedback", nil, token, params, &wire, false); err != nil {
		return ipc.Feedback_result{}, err
	}
	if wire.Accepted == nil || !*wire.Accepted || strings.TrimSpace(wire.Code) == "" {
		return ipc.Feedback_result{}, fmt.Errorf("%w: feedback receipt", Err_incomplete_response)
	}
	return ipc.Feedback_result{Accepted: true, Code: wire.Code}, nil
}
