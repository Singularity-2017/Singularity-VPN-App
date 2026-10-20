package api

import (
	"context"
	"errors"
	"net/http"
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

	// Reason is how the node was picked -- least_loaded, chosen_node or
	// lowest_rtt -- and Goals how each goal asked for was applied. Both are
	// shown, never acted on, and both are absent from a control plane too
	// old to send them.
	Reason string         `json:"reason"`
	Goals  []Goal_outcome `json:"goals"`
}

// Goal_outcome is the control plane's account of one goal: whether it
// shaped the recommendation and, when it did not, why.
type Goal_outcome struct {
	Goal    string `json:"goal"`
	Applied bool   `json:"applied"`
	Why     string `json:"why"`
	// Compared is, for an applied route goal, how many measured nodes the
	// pick was made among.
	Compared int `json:"compared"`
}

// Recommend_request is what a recommendation is asked for. The zero value
// asks for nothing in particular, which is what a control plane too old to
// know about goals answers anyway.
type Recommend_request struct {
	Anonymity string // "" off, or light, standard, high
	Bypass    bool
	Country   string // with a country, the geo goal
	Route     bool
	Client_id string // the connection the route goal measures
	Node_id   string // a server picked by hand
}

func (r Recommend_request) query() url.Values {
	q := url.Values{}
	if r.Anonymity != "" {
		q.Add("goal", "anonymity")
		q.Set("anonymity", r.Anonymity)
	}
	if r.Bypass {
		q.Add("goal", "bypass")
	}
	if r.Country != "" {
		q.Add("goal", "geo")
		q.Set("country", r.Country)
	}
	if r.Route {
		q.Add("goal", "route")
		if r.Client_id != "" {
			q.Set("client_id", r.Client_id)
		}
	}
	if r.Node_id != "" {
		q.Set("node_id", r.Node_id)
	}
	if len(q) == 0 {
		return nil
	}
	return q
}

// Server is one entry of the server list: what a person picks a server by,
// and whether it can take a connection now.
type Server struct {
	Node_id   string `json:"node_id"`
	Name      string `json:"name"`    // absent in the response means empty here
	Country   string `json:"country"` // absent in the response means empty here
	Region    string `json:"region"`
	City      string `json:"city"`
	Available bool   `json:"available"`
}

// Reach_target is one entry of the reachability catalog: a host the device
// probes directly to learn what the network under it blocks. A target with
// a Country is a control inside that jurisdiction -- the control plane
// hands it only to callers from there.
type Reach_target struct {
	Key     string `json:"key"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Country string `json:"country"` // absent in the response means empty here: not a control
	Label   string `json:"label"`
}

// Reach_catalog is what the control plane hands out to probe. Country is the
// caller's own country, empty when the control plane does not know it.
type Reach_catalog struct {
	Country string         `json:"country"`
	Targets []Reach_target `json:"targets"`
}

// validate lists the fields of a recommendation this client acts on.
// node_name, country, region and city are deliberately not among them: they
// are shown to a person rather than acted on, and the server omits region
// and city on purpose when nobody has typed them -- an unknown place is a
// real state, not an empty string. A rename of those is caught by the
// contract test in the control plane repository, because refusing to connect
// over a missing label would be the worse failure.
func (r Recommendation) validate() error {
	return check_required("the recommendation",
		required_field{"node_id", r.Node_id != ""},
		required_field{"protocol", r.Protocol != ""},
	)
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

// validate covers every field of a profile except its alternatives, which
// are a list the server may legitimately leave empty. Client_id first: it is
// the one value here that cannot be asked for again.
func (p Profile) validate() error {
	return check_required("the created profile",
		required_field{"client_id", p.Client_id != ""},
		required_field{"config", p.Config != ""},
		required_field{"node", p.Node_id != ""},
		required_field{"node_ip", p.Node_ip != ""},
		required_field{"protocol", p.Protocol != ""},
		required_field{"expires_at", !p.Expires_at.IsZero()},
	)
}

// Vpn_config is a config fetched for an already-provisioned client.
type Vpn_config struct {
	Config     string    `json:"config"`
	Protocol   string    `json:"protocol"`
	Mode       string    `json:"mode"`
	Expires_at time.Time `json:"expires_at"`
}

// validate refuses a config the client cannot act on. An expiry that did not
// arrive is not a small loss: it is the difference between a connection that
// warns before it lapses and one that simply stops.
func (v Vpn_config) validate() error {
	return check_required("the fetched config",
		required_field{"config", v.Config != ""},
		required_field{"protocol", v.Protocol != ""},
		required_field{"mode", v.Mode != ""},
		required_field{"expires_at", !v.Expires_at.IsZero()},
	)
}

// Device is what Mint_device_token returns: a token the service can use to
// authenticate before anybody logs in.
type Device struct {
	Device_id    string    `json:"device_id"`
	Device_token string    `json:"device_token"`
	Expires_at   time.Time `json:"expires_at"`
}

// validate refuses a device whose token or identity did not arrive. Both are
// stored, and a stored empty string would authenticate nothing while looking
// like a credential the service already holds.
func (d Device) validate() error {
	return check_required("the minted device token",
		required_field{"device_id", d.Device_id != ""},
		required_field{"device_token", d.Device_token != ""},
		required_field{"expires_at", !d.Expires_at.IsZero()},
	)
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
	if err := check_required("the login response",
		required_field{"token", response.Token != ""}); err != nil {
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
	if err := device.validate(); err != nil {
		return Device{}, err
	}
	return device, nil
}

// Recommend asks the control plane which node and protocol this client
// should use. The token may be a session token or a device token; both are
// sent the same way and this client does not need to tell them apart.
func (c *Client) Recommend(ctx context.Context, token string, request Recommend_request) (Recommendation, error) {
	var recommendation Recommendation
	if err := c.do(ctx, "GET", "/api/v1/vpn/recommend", request.query(), token, nil, &recommendation, true); err != nil {
		return Recommendation{}, err
	}
	if err := recommendation.validate(); err != nil {
		return Recommendation{}, err
	}
	return recommendation, nil
}

// Servers lists the servers this account may be put on. A server without
// a node_id cannot be picked and is refused as an incomplete response.
func (c *Client) Servers(ctx context.Context, token string) ([]Server, error) {
	var response struct {
		Servers []Server `json:"servers"`
	}
	if err := c.do(ctx, "GET", "/api/v1/vpn/servers", nil, token, nil, &response, true); err != nil {
		var api_err *Error
		if !errors.As(err, &api_err) || api_err.Status != http.StatusNotFound {
			return nil, err
		}
		// A control plane older than /vpn/servers lists the same nodes at
		// /api/v1/nodes, in its own shape.
		response.Servers, err = c.servers_from_nodes(ctx, token)
		if err != nil {
			return nil, err
		}
	}
	for _, s := range response.Servers {
		if err := check_required("a server", required_field{"node_id", s.Node_id != ""}); err != nil {
			return nil, err
		}
	}
	return response.Servers, nil
}

// servers_from_nodes reads the older /api/v1/nodes listing. Only what a
// Server carries is kept: the node's address in that listing is not this
// client's business and is dropped here. A node is available unless the
// control plane says it is offline.
func (c *Client) servers_from_nodes(ctx context.Context, token string) ([]Server, error) {
	var nodes []struct {
		Id      string  `json:"id"`
		Name    string  `json:"name"`
		Country string  `json:"country"`
		Status  string  `json:"status"`
		Region  *string `json:"region"`
		City    *string `json:"city"`
	}
	if err := c.do(ctx, "GET", "/api/v1/nodes", nil, token, nil, &nodes, true); err != nil {
		return nil, err
	}
	servers := make([]Server, 0, len(nodes))
	for _, n := range nodes {
		server := Server{Node_id: n.Id, Name: n.Name, Country: n.Country, Available: n.Status != "offline"}
		if n.Region != nil {
			server.Region = *n.Region
		}
		if n.City != nil {
			server.City = *n.City
		}
		servers = append(servers, server)
	}
	return servers, nil
}

// Reach_targets fetches the catalog of hosts to probe. A target without a
// key, a host or a usable port cannot be probed or reported on and is
// refused as an incomplete response, the way Servers refuses a server
// without a node_id.
func (c *Client) Reach_targets(ctx context.Context, token string) (Reach_catalog, error) {
	var catalog Reach_catalog
	if err := c.do(ctx, "GET", "/api/v1/reach/targets", nil, token, nil, &catalog, true); err != nil {
		return Reach_catalog{}, err
	}
	for _, t := range catalog.Targets {
		if err := check_required("a reach target",
			required_field{"key", t.Key != ""},
			required_field{"host", t.Host != ""},
			required_field{"port", t.Port > 0 && t.Port <= 65535},
		); err != nil {
			return Reach_catalog{}, err
		}
	}
	return catalog, nil
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
	if err := profile.validate(); err != nil {
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
	if err := config.validate(); err != nil {
		return Vpn_config{}, err
	}
	return config, nil
}
