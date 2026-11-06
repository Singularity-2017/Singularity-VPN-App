package ipc

import "context"

const Command_vless_options = "vless_options"
const Command_export_vless = "export_vless"

type Vless_node struct {
	Node_id         string `json:"node_id"`
	Vless_available bool   `json:"vless_available"`
	Reason          string `json:"reason,omitempty"`
}
type Vless_options_result struct {
	Nodes []Vless_node `json:"nodes"`
}
type Export_vless struct {
	Client_id string `json:"client_id"`
	Node_id   string `json:"node_id"`
}

func (p Export_vless) Validate() error {
	if err := (Set_profile{Id: p.Client_id}).Validate(); err != nil {
		return err
	}
	return (Set_profile{Id: p.Node_id}).Validate()
}

// The URI is delivered only to the native save action, never logged.
type Vless_result struct {
	Config string `json:"config"`
}

// Optional for compatibility with services predating profile exports.
type Vless_service interface {
	Vless_options(context.Context, Set_profile) (Vless_options_result, error)
	Export_vless(context.Context, Export_vless) (Vless_result, error)
}

func (c *Client) Vless_options(ctx context.Context, profile Set_profile) (Vless_options_result, error) {
	var result Vless_options_result
	resp, err := c.call(ctx, Command_vless_options, profile)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}
func (c *Client) Export_vless(ctx context.Context, params Export_vless) (Vless_result, error) {
	var result Vless_result
	resp, err := c.call(ctx, Command_export_vless, params)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}
