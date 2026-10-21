package api

import (
	"context"
	"net/url"
	"time"
)

// Account_profile is one of the account's connections, as GET /me lists
// them: what a device connects as. The account's client_limit counts them.
type Account_profile struct {
	Id         string    `json:"id"`
	Label      string    `json:"label"`
	Protocol   string    `json:"protocol"`
	Created_at time.Time `json:"created_at"`

	// Online is present only when the control plane's last poll of the
	// profile's node succeeded: true means some device is connected with
	// it now. Absent is "not known", never "offline".
	Online *bool `json:"online,omitempty"`
}

// Profiles lists the account's profiles, newest first.
func (c *Client) Profiles(ctx context.Context, token string) ([]Account_profile, error) {
	var me struct {
		Clients []Account_profile `json:"clients"`
	}
	if err := c.do(ctx, "GET", "/api/v1/me", nil, token, nil, &me, true); err != nil {
		return nil, err
	}
	return me.Clients, nil
}

// Label_profile names a profile, cosmetically: the node knows it by another
// name, and configs already handed out keep working. It returns the label
// the control plane stored.
func (c *Client) Label_profile(ctx context.Context, token, client_id, label string) (string, error) {
	query := url.Values{}
	query.Set("client_id", client_id)
	request := struct {
		Label string `json:"label"`
	}{label}
	var response struct {
		Label string `json:"label"`
	}
	if err := c.do(ctx, "PATCH", "/api/v1/vpn", query, token, request, &response, true); err != nil {
		return "", err
	}
	return response.Label, nil
}
