package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// list_test_service is a service that also keeps the list of services, in
// memory, and refuses what the real one refuses.
type list_test_service struct {
	*fake_service

	mu      sync.Mutex
	targets []Service_target
	removed int
	asked   []string // the commands it was asked, with their parameters
}

func new_list_test_service() *list_test_service {
	return &list_test_service{fake_service: new_fake_service(), targets: []Service_target{
		{Key: "bank_ru", Label: "Bank", Host: "bank.example", Port: 443, Popular: true, Control: true},
		{Key: "web_youtube", Label: "YouTube", Host: "www.youtube.com", Port: 443, Popular: true},
	}}
}

func (s *list_test_service) result() Services_result {
	return Services_result{Targets: append([]Service_target{}, s.targets...), Removed_popular: s.removed}
}

func (s *list_test_service) Services(context.Context) (Services_result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, "services")
	return s.result(), nil
}

func (s *list_test_service) Remove_service(_ context.Context, params Remove_service_params) (Services_result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, "remove_service "+params.Key)
	for i, t := range s.targets {
		if t.Key != params.Key {
			continue
		}
		if t.Control {
			return Services_result{}, fmt.Errorf("wrapped: %w", Err_service_not_removable)
		}
		s.targets = append(s.targets[:i], s.targets[i+1:]...)
		s.removed++
		return s.result(), nil
	}
	return Services_result{}, Err_unknown_service
}

func (s *list_test_service) Add_service(_ context.Context, params Add_service_params) (Services_result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, "add_service "+params.Host)
	switch {
	case !strings.Contains(params.Host, "."):
		return Services_result{}, Err_invalid_host
	case params.Host == "full.example.org":
		return Services_result{}, Err_too_many_services
	}
	for _, t := range s.targets {
		if t.Host == params.Host {
			return Services_result{}, Err_duplicate_service
		}
	}
	s.targets = append(s.targets, Service_target{Key: "custom:" + params.Host + ":443", Label: params.Host, Host: params.Host, Port: 443})
	return s.result(), nil
}

func (s *list_test_service) Restore_services(context.Context) (Services_result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, "restore_services")
	if s.removed > 0 {
		s.targets = append(s.targets, Service_target{Key: "web_x", Label: "X", Host: "x.com", Port: 443, Popular: true})
		s.removed = 0
	}
	return s.result(), nil
}

func Test_the_list_commands_round_trip(t *testing.T) {
	service := new_list_test_service()
	client, _ := serve_pair(t, service)
	ctx := with_timeout(t)

	got, err := client.Services(ctx)
	if err != nil || len(got.Targets) != 2 || got.Removed_popular != 0 || !got.Targets[0].Control || got.Targets[1].Control || !got.Targets[1].Popular {
		t.Fatalf("Services() = %+v, %v", got, err)
	}
	if got.Targets[1] != (Service_target{Key: "web_youtube", Label: "YouTube", Host: "www.youtube.com", Port: 443, Popular: true}) {
		t.Fatalf("a target lost something in transit: %+v", got.Targets[1])
	}

	got, err = client.Remove_service(ctx, Remove_service_params{Key: "web_youtube"})
	if err != nil || len(got.Targets) != 1 || got.Removed_popular != 1 {
		t.Fatalf("Remove_service() = %+v, %v", got, err)
	}
	got, err = client.Add_service(ctx, Add_service_params{Host: "example.org"})
	if err != nil || len(got.Targets) != 2 || got.Targets[1].Key != "custom:example.org:443" || got.Targets[1].Popular || got.Removed_popular != 1 {
		t.Fatalf("Add_service() = %+v, %v", got, err)
	}
	got, err = client.Restore_services(ctx)
	if err != nil || len(got.Targets) != 3 || got.Removed_popular != 0 {
		t.Fatalf("Restore_services() = %+v, %v", got, err)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	want := "services|remove_service web_youtube|add_service example.org|restore_services"
	if asked := strings.Join(service.asked, "|"); asked != want {
		t.Fatalf("the service was asked %q, want %q", asked, want)
	}
}

func Test_the_list_wire_shape(t *testing.T) {
	raw, err := json.Marshal(Services_result{
		Targets:         []Service_target{{Key: "web_youtube", Label: "YouTube", Host: "www.youtube.com", Port: 443, Popular: true}},
		Removed_popular: 2,
	})
	want := `{"targets":[{"key":"web_youtube","label":"YouTube","host":"www.youtube.com","port":443,"popular":true,"control":false}],"removed_popular":2}`
	if err != nil || string(raw) != want {
		t.Fatalf("services answer = %s, %v; want %s", raw, err, want)
	}
	for _, c := range []struct {
		command string
		params  any
		want    string
	}{
		{Command_remove_service, Remove_service_params{Key: "k"}, `{"key":"k"}`},
		{Command_add_service, Add_service_params{Host: "example.org"}, `{"host":"example.org"}`},
	} {
		raw, err := json.Marshal(c.params)
		if err != nil || string(raw) != c.want {
			t.Fatalf("%s params = %s, %v; want %s", c.command, raw, err, c.want)
		}
	}
	if Command_services != "services" || Command_remove_service != "remove_service" || Command_add_service != "add_service" || Command_restore_services != "restore_services" {
		t.Fatal("a list command was renamed; the page and the docs call these")
	}
}

func Test_a_refused_list_edit_arrives_as_its_code(t *testing.T) {
	client, _ := serve_pair(t, new_list_test_service())
	ctx := with_timeout(t)

	cases := []struct {
		name   string
		call   func() error
		code   string
		remote *Remote_error
	}{
		{"an invalid host", func() error { _, err := client.Add_service(ctx, Add_service_params{Host: "localhost"}); return err }, "invalid_host", Remote_invalid_host},
		{"an empty host is invalid, not a bad request", func() error { _, err := client.Add_service(ctx, Add_service_params{}); return err }, "invalid_host", Remote_invalid_host},
		{"a full list", func() error {
			_, err := client.Add_service(ctx, Add_service_params{Host: "full.example.org"})
			return err
		}, "too_many", Remote_too_many},
		{"a duplicate", func() error { _, err := client.Add_service(ctx, Add_service_params{Host: "bank.example"}); return err }, "duplicate", Remote_duplicate},
		{"an unknown key", func() error { _, err := client.Remove_service(ctx, Remove_service_params{Key: "nothing"}); return err }, "unknown_service", Remote_unknown_service},
		{"a control, wrapped", func() error { _, err := client.Remove_service(ctx, Remove_service_params{Key: "bank_ru"}); return err }, "not_removable", Remote_not_removable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			var remote *Remote_error
			if !errors.As(err, &remote) || remote.Code != c.code {
				t.Fatalf("error = %v, want code %q", err, c.code)
			}
			if !errors.Is(err, c.remote) {
				t.Fatalf("errors.Is(%v, the %s sentinel) = false", err, c.code)
			}
			if remote.Message == "" {
				t.Fatal("the refusal has no message")
			}
		})
	}
}

func Test_list_parameters_are_checked_at_the_boundary(t *testing.T) {
	service := new_list_test_service()
	client, _ := serve_pair(t, service)
	ctx := with_timeout(t)

	cases := []struct {
		name    string
		command string
		params  any
	}{
		{"remove without a key", Command_remove_service, Remove_service_params{}},
		{"remove with an absurd key", Command_remove_service, Remove_service_params{Key: strings.Repeat("k", max_service_param_bytes+1)}},
		{"add with an absurd host", Command_add_service, Add_service_params{Host: strings.Repeat("h", max_service_param_bytes+1)}},
		{"remove with a number for the key", Command_remove_service, map[string]any{"key": 7}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := client.call(ctx, c.command, c.params)
			if err != nil {
				t.Fatal(err)
			}
			var remote *Remote_error
			if err := decode_result(resp, nil); !errors.As(err, &remote) || remote.Code != Error_bad_request {
				t.Fatalf("answer = %v, want bad_request", err)
			}
		})
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.asked) != 0 {
		t.Fatalf("the service was asked %v although the parameters were refused", service.asked)
	}
}

func Test_a_service_without_the_list_refuses_the_list_commands(t *testing.T) {
	client, _ := serve_pair(t, new_fake_service())
	ctx := with_timeout(t)

	calls := map[string]func() error{
		"services":         func() error { _, err := client.Services(ctx); return err },
		"remove_service":   func() error { _, err := client.Remove_service(ctx, Remove_service_params{Key: "k"}); return err },
		"add_service":      func() error { _, err := client.Add_service(ctx, Add_service_params{Host: "example.org"}); return err },
		"restore_services": func() error { _, err := client.Restore_services(ctx); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("%s was accepted by a service that does not keep the list", name)
		}
	}
	// And the connection is still good.
	if _, err := client.Status(ctx); err != nil {
		t.Fatalf("Status() after the refusals = %v", err)
	}
}
