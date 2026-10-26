package ipc

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const Command_account = "account"
const Command_feedback = "feedback"
const Command_spending_page = "spending_page"

type Feedback_params struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

func (p Feedback_params) Validate() error {
	if p.Kind != "problem" && p.Kind != "idea" {
		return errors.New("invalid feedback kind")
	}
	message := strings.TrimSpace(p.Message)
	if !utf8.ValidString(message) || message == "" || utf8.RuneCountInString(message) > 2000 {
		return errors.New("invalid feedback message")
	}
	return nil
}

type Feedback_result struct {
	Accepted bool   `json:"accepted"`
	Code     string `json:"code"`
}

type Feedback_service interface {
	Feedback(context.Context, Feedback_params) (Feedback_result, error)
}

func (c *Client) Feedback(ctx context.Context, params Feedback_params) (Feedback_result, error) {
	var result Feedback_result
	if err := params.Validate(); err != nil {
		return result, err
	}
	resp, err := c.call(ctx, Command_feedback, params)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

// Account_spending is one page of the account's own recorded paid payments.
// Totals come from the control plane for the whole history; the client never
// adds pages, allocations or tariffs into them. Totals is nil unless Status is
// "available", and an empty slice there is a verified history with no payments.
type Account_spending struct {
	Status string                   `json:"status"`
	Reason string                   `json:"reason"`
	Totals []Account_currency_total `json:"totals"`
	// all_recorded_paid_payments, or verified_paid_payments_only while the
	// total is unavailable: then the pages are not the whole history.
	Coverage     string                `json:"coverage"`
	Transactions []Account_transaction `json:"transactions"`
	// Opaque and bound to this account and snapshot; empty on the last page.
	Next_cursor string `json:"next_cursor"`
	// The old traversal was void (history changed, cursor expired), so this
	// is a first page again: it replaces the list instead of extending it.
	Restarted bool `json:"restarted,omitempty"`
}

type Account_currency_total struct {
	Currency     string `json:"currency"`
	Amount_minor int64  `json:"amount_minor"`
}

type Account_transaction struct {
	Payment_id   string    `json:"payment_id"`
	Amount_minor int64     `json:"amount_minor"`
	Currency     string    `json:"currency"`
	Occurred_at  time.Time `json:"occurred_at"`
}

type Spending_page_params struct {
	Cursor string `json:"cursor"`
}

func (p Spending_page_params) Validate() error {
	if p.Cursor == "" || len(p.Cursor) > 4096 {
		return errors.New("invalid spending cursor")
	}
	return nil
}

// Spending_service is optional like Account_service.
type Spending_service interface {
	Spending_page(context.Context, Spending_page_params) (Account_spending, error)
}

func (c *Client) Spending_page(ctx context.Context, params Spending_page_params) (Account_spending, error) {
	var result Account_spending
	if err := params.Validate(); err != nil {
		return result, err
	}
	resp, err := c.call(ctx, Command_spending_page, params)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

// Account_data contains display data only, never credentials or configurations.
type Account_data struct {
	Spending             Account_spending              `json:"spending"`
	User_id              string                        `json:"user_id"`
	Username             string                        `json:"username"`
	Plan                 Account_plan                  `json:"plan"`
	Credits              *float64                      `json:"credits"`
	Referral_code        string                        `json:"referral_code"`
	Invite_tickets       *int                          `json:"invite_tickets"`
	Referrer             *Account_referrer             `json:"referrer"`
	Network_score        *int                          `json:"network_score"`
	Sponsored_used       *int                          `json:"sponsored_used"`
	Sponsored_total      *int                          `json:"sponsored_total"`
	Sponsored_plan       *string                       `json:"sponsored_plan"`
	Payments             []Account_payment             `json:"payments"`
	Referrals            []Account_referral            `json:"referrals"`
	Devices              []Account_device              `json:"devices"`
	Reports              []Account_report              `json:"reports"`
	Contribution_summary *Account_contribution_summary `json:"contribution_summary"`
	Section_errors       map[string]string             `json:"section_errors,omitempty"`
}

type Account_contribution struct {
	Reports []Account_report              `json:"reports"`
	Summary *Account_contribution_summary `json:"summary"`
}

type Account_report struct {
	Code         string     `json:"code"`
	Created_at   *time.Time `json:"created_at"`
	Kind         string     `json:"kind"`
	Message      string     `json:"message"`
	Verdict      string     `json:"verdict"`
	Score        *int       `json:"score"`
	Score_source string     `json:"score_source"`
	Score_reason string     `json:"score_reason"`
	Fixed_commit string     `json:"fixed_commit"`
}

type Account_contribution_summary struct {
	Reports *int `json:"reports"`
	Bugs    *int `json:"bugs"`
	Fixed   *int `json:"fixed"`
	Score   *int `json:"score"`
}

type Account_referrer struct {
	Username string `json:"username"`
}

type Account_plan struct {
	Id                  string     `json:"id"`
	Name                string     `json:"name"`
	Expires_at          *time.Time `json:"expires_at"`
	Client_limit        *int       `json:"client_limit"`
	Subscription_active bool       `json:"subscription_active"`
	Sponsored_place     *bool      `json:"sponsored_place"`
	// Tariff floor and target, not a measured connection speed or a promise on every node.
	Rate_kbps           *int64  `json:"rate_kbps"`
	Target_kbps         *int64  `json:"target_kbps"`
	Effective_rate_kbps *int64  `json:"effective_rate_kbps"`
	Effective_ceil_kbps *int64  `json:"effective_ceil_kbps"`
	Personal_bandwidth  *bool   `json:"personal_bandwidth"`
	Amount_minor        *int64  `json:"amount_minor"`
	Currency            *string `json:"currency"`
	Period_days         *int    `json:"period_days"`
}

type Account_payment struct {
	Payment_id           string     `json:"payment_id"`
	Id                   string     `json:"id"`
	Amount_minor         *int64     `json:"amount_minor"`
	Payment_amount_minor *int64     `json:"payment_amount_minor"`
	Plan                 string     `json:"plan"`
	Period_start         *time.Time `json:"period_start"`
	Period_end           *time.Time `json:"period_end"`
	Payment_occurred_at  *time.Time `json:"payment_occurred_at"`
	Payment_currency     string     `json:"payment_currency"`
}

type Account_referral struct {
	Referee_id       string     `json:"referee_id"`
	Referee_username string     `json:"referee_username"`
	Created_at       *time.Time `json:"created_at"`
	Sponsored        *bool      `json:"sponsored"`
}

type Account_device struct {
	Current      bool       `json:"current"`
	Device_id    string     `json:"device_id"`
	Name         string     `json:"name"`
	Created_at   *time.Time `json:"created_at"`
	Last_seen_at *time.Time `json:"last_seen_at"`
	Expires_at   *time.Time `json:"expires_at"`
	Revoked_at   *time.Time `json:"revoked_at"`
}

// Account_service is optional so older services and test doubles remain usable.
type Account_service interface {
	Account(context.Context) (Account_data, error)
}

func (c *Client) Account(ctx context.Context) (Account_data, error) {
	var result Account_data
	resp, err := c.call(ctx, Command_account, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}
