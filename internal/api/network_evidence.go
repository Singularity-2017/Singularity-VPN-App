package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type Network_context struct {
	Context_id  string    `json:"context_id"`
	Scope       string    `json:"scope"`
	Country     string    `json:"country,omitempty"`
	Asn         string    `json:"asn,omitempty"`
	Region      string    `json:"region,omitempty"`
	Captured_at time.Time `json:"captured_at"`
	Expires_at  time.Time `json:"expires_at"`
}

type Connection_observation struct {
	Observation_id   string    `json:"observation_id"`
	Context_id       string    `json:"context_id"`
	Client_id        string    `json:"client_id"`
	Node_id          string    `json:"node_id"`
	Protocol         string    `json:"protocol"`
	Profile_version  string    `json:"profile_version,omitempty"`
	Mode             string    `json:"mode"`
	Session_id       string    `json:"session_id"`
	Observed_at      time.Time `json:"observed_at"`
	Kind             string    `json:"kind"`
	Reason           string    `json:"reason"`
	Duration_seconds *float64  `json:"duration_seconds,omitempty"`
}

type Connection_evidence struct {
	Source               string    `json:"source"`
	Node_id              string    `json:"node_id"`
	Protocol             string    `json:"protocol"`
	Mode                 string    `json:"mode"`
	Profile_version      string    `json:"profile_version,omitempty"`
	Scope                string    `json:"scope"`
	Country              string    `json:"country,omitempty"`
	Asn                  string    `json:"asn,omitempty"`
	Region               string    `json:"region,omitempty"`
	Window_start         time.Time `json:"window_start"`
	Window_end           time.Time `json:"window_end"`
	Observed_at          time.Time `json:"observed_at"`
	Attempts             int       `json:"attempts"`
	Established          int       `json:"established"`
	Failures             int       `json:"failures"`
	Ended_sessions       int       `json:"ended_sessions"`
	Active_seconds       *float64  `json:"active_seconds,omitempty"`
	Observation_count    int       `json:"observation_count"`
	Transfer_samples     int       `json:"transfer_samples"`
	Observer_unavailable int       `json:"observer_unavailable"`
	Counter_resets       int       `json:"counter_resets"`
	// Reported counts the sessions the person sent a problem report about;
	// each is already one of Failures. A control plane older than reports
	// sends none.
	Reported int `json:"reported,omitempty"`
}

type Observation_ack struct {
	Accepted_ids []string              `json:"accepted_ids"`
	Evidence     []Connection_evidence `json:"evidence"`
}

// Optional endpoints never turn an older control plane into a connection failure.
func Optional_unavailable(err error) bool {
	var api_error *Error
	return errors.As(err, &api_error) && (api_error.Status == http.StatusNotFound || api_error.Status == http.StatusMethodNotAllowed)
}

// Capture_network_context uses only the explicitly supplied physical transport.
func (c *Client) Capture_network_context(ctx context.Context, token string, direct *http.Client) (Network_context, error) {
	if direct == nil {
		return Network_context{}, fmt.Errorf("api: physical transport is required")
	}
	copy_client := *c
	copy_client.http_client = direct
	var result Network_context
	if err := copy_client.do(ctx, http.MethodPost, "/api/v1/network-context", nil, token, struct{}{}, &result, false); err != nil {
		return Network_context{}, err
	}
	if result.Context_id == "" || !valid_context_scope(result.Scope) || result.Captured_at.IsZero() || !result.Expires_at.After(result.Captured_at) || (result.Scope == "asn" && result.Asn == "") || (result.Scope == "country" && result.Country == "") {
		return Network_context{}, fmt.Errorf("api: incomplete network context")
	}
	return result, nil
}

func valid_context_scope(scope string) bool {
	return scope == "asn" || scope == "country" || scope == "unknown"
}

func (c *Client) Send_observations(ctx context.Context, token string, observations []Connection_observation) (Observation_ack, error) {
	var result Observation_ack
	if err := c.do(ctx, http.MethodPost, "/api/v1/vpn/observations", nil, token, struct {
		Observations []Connection_observation `json:"observations"`
	}{observations}, &result, false); err != nil {
		return result, err
	}
	if result.Accepted_ids == nil {
		return Observation_ack{}, fmt.Errorf("api: observation acknowledgement is missing")
	}
	offered := map[string]bool{}
	for _, event := range observations {
		offered[event.Observation_id] = true
	}
	for _, id := range result.Accepted_ids {
		if !offered[id] {
			return Observation_ack{}, fmt.Errorf("api: acknowledgement contains an unknown observation")
		}
	}
	for _, row := range result.Evidence {
		if err := row.Validate(); err != nil {
			return Observation_ack{}, err
		}
	}
	return result, nil
}

func (e Connection_evidence) Validate() error {
	if (e.Source != "app" && e.Source != "server") || e.Node_id == "" || e.Protocol == "" || e.Mode == "" || !valid_context_scope(e.Scope) || e.Observed_at.IsZero() || e.Window_start.IsZero() || e.Window_end.Before(e.Window_start) || e.Attempts < 0 || e.Established < 0 || e.Failures < 0 || e.Ended_sessions < 0 || e.Observation_count < 0 || e.Transfer_samples < 0 || e.Observer_unavailable < 0 || e.Counter_resets < 0 || (e.Active_seconds != nil && *e.Active_seconds < 0) {
		return fmt.Errorf("api: incomplete connection evidence")
	}
	return nil
}

// Missing counters are incomplete data, rather than evidence of zero failures.
func (e *Connection_evidence) UnmarshalJSON(data []byte) error {
	type wire Connection_evidence
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"attempts", "established", "failures", "ended_sessions"} {
		if raw, ok := fields[name]; !ok || string(raw) == "null" {
			return fmt.Errorf("%w: connection evidence counter %s is missing", Err_incomplete_response, name)
		}
	}
	*e = Connection_evidence(decoded)
	return e.Validate()
}
