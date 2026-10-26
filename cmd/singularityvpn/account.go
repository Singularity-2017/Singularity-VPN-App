package main

import (
	"context"

	"singularity-vpn-app/internal/ipc"
)

type Account_view struct {
	Account *ipc.Account_data `json:"account"`
	Error   *Outcome          `json:"error"`
}

func (a *App) Account() Account_view {
	var view Account_view
	outcome := a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		result, err := c.Account(ctx)
		if err == nil {
			view.Account = &result
		}
		return err
	})
	if outcome.Code != "" {
		view.Error = &outcome
	}
	return view
}

type Spending_view struct {
	Spending *ipc.Account_spending `json:"spending"`
	Error    *Outcome              `json:"error"`
}

func (a *App) Spending_page(cursor string) Spending_view {
	var view Spending_view
	outcome := a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		result, err := c.Spending_page(ctx, ipc.Spending_page_params{Cursor: cursor})
		if err == nil {
			view.Spending = &result
		}
		return err
	})
	if outcome.Code != "" {
		view.Error = &outcome
	}
	return view
}

type Feedback_view struct {
	Feedback *ipc.Feedback_result `json:"feedback"`
	Error    *Outcome             `json:"error"`
}

func (a *App) Feedback(params ipc.Feedback_params) Feedback_view {
	var view Feedback_view
	outcome := a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		result, err := c.Feedback(ctx, params)
		if err == nil {
			view.Feedback = &result
		}
		return err
	})
	if outcome.Code != "" {
		view.Error = &outcome
	}
	return view
}
