package main

import (
	"context"
	"time"

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

type Problem_draft_view struct {
	Draft *ipc.Problem_draft `json:"draft"`
	Error *Outcome           `json:"error"`
}

// Problem_draft asks the service to assemble the attachments of a problem
// report. Only this process can see the desktop, so when include_apps is set
// it hands the service the programs that were in front; the person opts in to
// that separately from the rest, and without it no program is named. Nothing
// is sent; Feedback sends, naming the draft.
func (a *App) Problem_draft(include_apps bool) Problem_draft_view {
	var view Problem_draft_view
	var apps []ipc.App_use
	if include_apps {
		apps = a.foreground.snapshot(time.Now())
	}
	outcome := a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		result, err := c.Problem_draft(ctx, ipc.Problem_draft_params{Apps: apps})
		if err == nil {
			view.Draft = &result
		}
		return err
	})
	if outcome.Code != "" {
		view.Error = &outcome
	}
	return view
}
