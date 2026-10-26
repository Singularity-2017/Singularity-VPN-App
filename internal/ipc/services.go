package ipc

import (
	"context"
	"errors"
	"fmt"
)

// The commands that edit the list of services the availability analysis
// probes. Like Command_account they are served only by a Service that also
// implements Reach_list_service, so older services and test doubles stay
// usable.
const (
	Command_services         = "services"
	Command_remove_service   = "remove_service"
	Command_add_service      = "add_service"
	Command_restore_services = "restore_services"
)

// Error codes the list commands add to protocol.go's set. Each is a refusal
// the window can word for the person: the host they typed is not a host name,
// the list is full, the host is already in it, the key names nothing in it, or
// the entry is a control the measurement cannot do without.
const (
	Error_invalid_host    = "invalid_host"
	Error_too_many        = "too_many"
	Error_duplicate       = "duplicate"
	Error_unknown_service = "unknown_service"
	Error_not_removable   = "not_removable"
)

// The sentinels a Reach_list_service returns; error_body_for maps them to the
// codes above.
var (
	Err_invalid_host          = errors.New("ipc: the host is not a valid host name")
	Err_too_many_services     = errors.New("ipc: the list already holds as many added services as it may")
	Err_duplicate_service     = errors.New("ipc: the service is already in the list")
	Err_unknown_service       = errors.New("ipc: there is no such service in the list")
	Err_service_not_removable = errors.New("ipc: this service is part of the measurement itself and cannot be removed")
)

// The sentinels a caller matches a Remote_error against with errors.Is.
var (
	Remote_invalid_host    = &Remote_error{Code: Error_invalid_host}
	Remote_too_many        = &Remote_error{Code: Error_too_many}
	Remote_duplicate       = &Remote_error{Code: Error_duplicate}
	Remote_unknown_service = &Remote_error{Code: Error_unknown_service}
	Remote_not_removable   = &Remote_error{Code: Error_not_removable}
)

// Service_target is one service in the effective list. Popular says it came
// from the control plane's catalog, as opposed to being added by the person.
// Control marks a host inside the person's own country that the measurement
// needs as a reference: it is shown, if at all, as fixed, and cannot be
// removed.
type Service_target struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Popular bool   `json:"popular"`
	Control bool   `json:"control"`
}

// Services_result answers every list command: the list as it stands now.
// Removed_popular is how many catalog entries the person has removed, which is
// what a "restore popular" button needs to know to show itself.
type Services_result struct {
	Targets         []Service_target `json:"targets"`
	Removed_popular int              `json:"removed_popular"`
}

// Remove_service_params is Command_remove_service's parameter: the key of a
// Service_target.
type Remove_service_params struct {
	Key string `json:"key"`
}

// max_service_param_bytes bounds what a list command may carry; the real
// limits (a host name's length, a key's shape) are the service's to judge, so
// that the person is told which rule they broke.
const max_service_param_bytes = 1024

func (p Remove_service_params) Validate() error {
	if p.Key == "" || len(p.Key) > max_service_param_bytes {
		return fmt.Errorf("ipc: remove_service params: key of %d bytes", len(p.Key))
	}
	return nil
}

// Add_service_params is Command_add_service's parameter: what the person
// typed. An empty or malformed host is answered Error_invalid_host by the
// service, not refused here as a bad request, so the window can say why.
type Add_service_params struct {
	Host string `json:"host"`
}

func (p Add_service_params) Validate() error {
	if len(p.Host) > max_service_param_bytes {
		return fmt.Errorf("ipc: add_service params: host of %d bytes", len(p.Host))
	}
	return nil
}

// Reach_list_service is optional so older services and test doubles remain
// usable. Every method answers with the new list, or fails and changes
// nothing.
type Reach_list_service interface {
	Services(context.Context) (Services_result, error)
	Remove_service(context.Context, Remove_service_params) (Services_result, error)
	Add_service(context.Context, Add_service_params) (Services_result, error)
	Restore_services(context.Context) (Services_result, error)
}

func (c *Client) Services(ctx context.Context) (Services_result, error) {
	var result Services_result
	resp, err := c.call(ctx, Command_services, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Remove_service(ctx context.Context, params Remove_service_params) (Services_result, error) {
	var result Services_result
	resp, err := c.call(ctx, Command_remove_service, params)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Add_service(ctx context.Context, params Add_service_params) (Services_result, error) {
	var result Services_result
	resp, err := c.call(ctx, Command_add_service, params)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Restore_services(ctx context.Context) (Services_result, error) {
	var result Services_result
	resp, err := c.call(ctx, Command_restore_services, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

// reach_list_service returns the service as a Reach_list_service, or answers
// the request with the refusal for a service that is not one.
func (h *conn_handler) reach_list_service(id string) (Reach_list_service, bool) {
	service, ok := h.service.(Reach_list_service)
	if !ok {
		h.reply(id, nil, errors.New("the list of services is not supported by this service"))
	}
	return service, ok
}
