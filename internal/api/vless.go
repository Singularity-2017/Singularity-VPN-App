package api

import (
	"context"
	"errors"
	"net/url"
	"singularity-vpn-app/internal/protocols/vless"
	"strings"
)

type Vless_node struct {
	Node_id         string `json:"node_id"`
	Vless_available bool   `json:"vless_available"`
	Reason          string `json:"reason,omitempty"`
}

// Native connections share the authenticated, ownership-checked export fetch,
// then apply the stricter native transport contract before touching networking.
func (c *Client) fetch_native_vless(ctx context.Context, token, client_id, node_id string) (Vpn_config, error) {
	config, err := c.Fetch_vless(ctx, token, client_id, node_id)
	if err != nil {
		return Vpn_config{}, err
	}
	if _, err := vless.Parse_uri([]byte(config.Config)); err != nil {
		return Vpn_config{}, err
	}
	return config, nil
}

type Vless_options struct {
	Nodes []Vless_node `json:"nodes"`
}

// Vless_options discovers export capability for an existing owned profile.
// Missing capabilities and older servers fail closed.
func (c *Client) Vless_options(ctx context.Context, token, client_id string) (Vless_options, error) {
	if client_id == "" {
		return Vless_options{}, errors.New("api: profile is required")
	}
	var result Vless_options
	err := c.do(ctx, "GET", "/api/v1/vpn/vless/options", url.Values{"client_id": {client_id}}, token, nil, &result, true)
	return result, err
}

// Fetch_vless creates an alternative export, never a profile or a tunnel.
func (c *Client) Fetch_vless(ctx context.Context, token, client_id, node_id string) (Vpn_config, error) {
	options, err := c.Vless_options(ctx, token, client_id)
	if err != nil {
		return Vpn_config{}, err
	}
	available := false
	for _, node := range options.Nodes {
		if node.Node_id == node_id && node_id != "" && node.Vless_available {
			available = true
		}
	}
	if !available {
		return Vpn_config{}, errors.New("api: VLESS export is unavailable for this profile and server")
	}
	var config Vpn_config
	query := url.Values{"client_id": {client_id}, "node_id": {node_id}, "protocol": {"vless"}, "mode": {"vpn"}}
	if err := c.do(ctx, "GET", "/api/v1/vpn/config", query, token, nil, &config, true); err != nil {
		return Vpn_config{}, err
	}
	if err := config.validate(); err != nil {
		return Vpn_config{}, err
	}
	uri, parse_err := url.Parse(config.Config)
	if config.Protocol != "vless" || config.Mode != "vpn" || parse_err != nil || uri.Scheme != "vless" || uri.Hostname() == "" || uri.Port() == "" || uri.User == nil || uri.User.Username() == "" || strings.ContainsAny(config.Config, "\r\n\t ") {
		return Vpn_config{}, errors.New("api: invalid VLESS export")
	}
	return config, nil
}
