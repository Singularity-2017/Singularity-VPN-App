package api

import (
	"context"
	"net/url"
	"time"
)

// Alternative is one fallback the control plane names alongside its primary
// choice -- a node or protocol the client can fall back to if the primary
// becomes unreachable.
type Alternative struct {
	Node     string `json:"node"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
}

// Recommendation is the control plane's answer to "where should this client
// connect". The server decides; the client receives a decision and never
// makes one itself.
type Recommendation struct {
	Node_id   string `json:"node_id"`
	Node_name string `json:"node_name"`
	Country   string `json:"country"`
	Region    string `json:"region"` // absent in the response means empty here
	City      string `json:"city"`   // absent in the response means empty here
	Protocol  string `json:"protocol"`

	Alternatives []Alternative `json:"alternatives"`
}

// Profile is what Create_profile provisions. Its Client_id belongs in local
// state, because Create_profile itself must run at most once per
// installation.
type Profile struct {
	Client_id string `json:"client_id"`
	Config    string `json:"config"`
	Node_id   string `json:"node"` // JSON field is "node"
	Node_ip   string `json:"node_ip"`
	Protocol  string `json:"protocol"`

	Expires_at   time.Time     `json:"expires_at"`
	Alternatives []Alternative `json:"alternatives"`
}

// Vpn_config is a config fetched for an already-provisioned client.
type Vpn_config struct {
	Config     string    `json:"config"`
	Protocol   string    `json:"protocol"`
	Mode       string    `json:"mode"`
	Expires_at time.Time `json:"expires_at"`
}

// Device is what Mint_device_token returns: a token the service can use to
// authenticate before anybody logs in.
type Device struct {
	Device_id    string    `json:"device_id"`
	Device_token string    `json:"device_token"`
	Expires_at   time.Time `json:"expires_at"`
}

// Login exchanges a username and password for a session token. Wrong
// credentials come back as an *Error carrying Code_unauthorized.
func (c *Client) Login(ctx context.Context, username, password string) (string, error) {
	request := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{username, password}

	var response struct {
		Token string `json:"token"`
	}
	if err := c.do(ctx, "POST", "/api/v1/auth/login", nil, "", request, &response, true); err != nil {
		return "", err
	}
	return response.Token, nil
}

// Mint_device_token asks the control plane for a token the service can hold
// long-term. A device token may not mint another one -- the server answers
// 403 DEVICE_TOKEN_CANNOT_MINT, and this method surfaces that rather than
// special-casing it.
//
// Like Create_profile, this call is never retried. A retry after a lost
// response mints a second token that nobody holds and nothing will ever
// use, while it stays valid on the account until its expiry -- a live
// credential the person cannot know to revoke. Reporting the lost answer
// and minting again deliberately is the cheaper mistake.
func (c *Client) Mint_device_token(ctx context.Context, session_token, name string) (Device, error) {
	request := struct {
		Name string `json:"name"`
	}{name}

	var device Device
	if err := c.do(ctx, "POST", "/api/v1/auth/device", nil, session_token, request, &device, false); err != nil {
		return Device{}, err
	}
	return device, nil
}

// Recommend asks the control plane which node and protocol this client
// should use. The token may be a session token or a device token; both are
// sent the same way and this client does not need to tell them apart.
func (c *Client) Recommend(ctx context.Context, token string) (Recommendation, error) {
	var recommendation Recommendation
	if err := c.do(ctx, "GET", "/api/v1/vpn/recommend", nil, token, nil, &recommendation, true); err != nil {
		return Recommendation{}, err
	}
	return recommendation, nil
}

// Create_profile provisions a profile on the control plane. Call it once per
// installation: the account's client_limit counts profiles, so calling it
// again to recover from a lost response costs the person a slot they cannot
// get back. That is also why this call is never retried by this client --
// see Test_create_profile_is_never_retried. The Client_id it returns belongs
// in local state; every later connection fetches a config for that id
// through Fetch_config instead of provisioning again.
//
// protocol and node_id are omitted from the request when empty, so the
// server picks.
func (c *Client) Create_profile(ctx context.Context, token, protocol, node_id string, duration_days int) (Profile, error) {
	request := struct {
		Protocol      string `json:"protocol,omitempty"`
		Node_id       string `json:"node_id,omitempty"`
		Duration_days int    `json:"duration_days,omitempty"`
	}{protocol, node_id, duration_days}

	var profile Profile
	if err := c.do(ctx, "POST", "/api/v1/vpn/auto", nil, token, request, &profile, false); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// Fetch_config retrieves a config for an already-created profile.
// Code_client_not_found here means the stored client_id no longer exists on
// the control plane and a profile must be provisioned again; deciding that
// is stage 2's job, not this client's.
//
// Mode is not a parameter this client sends: full VPN is the server's
// default, and the split-tunnel mode does not exist in v1. client_id,
// protocol and node_id are omitted from the query when empty rather than
// sent as empty values.
func (c *Client) Fetch_config(ctx context.Context, token, client_id, protocol, node_id string) (Vpn_config, error) {
	query := url.Values{}
	if client_id != "" {
		query.Set("client_id", client_id)
	}
	if protocol != "" {
		query.Set("protocol", protocol)
	}
	if node_id != "" {
		query.Set("node_id", node_id)
	}

	var config Vpn_config
	if err := c.do(ctx, "GET", "/api/v1/vpn/config", query, token, nil, &config, true); err != nil {
		return Vpn_config{}, err
	}
	return config, nil
}
