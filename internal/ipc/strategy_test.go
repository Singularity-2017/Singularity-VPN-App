package ipc

import (
	"errors"
	"testing"
)

func Test_a_strategy_with_a_value_the_service_would_not_ask_for_is_refused(t *testing.T) {
	for _, bad := range []Strategy{
		{Anonymity: "max"},
		{Country: "de"},
		{Country: "DEU"},
		{Node_id: "node 2"},
		{Node_id: "../node"},
	} {
		if bad.Validate() == nil {
			t.Errorf("Validate(%+v) accepted", bad)
		}
	}
	for _, good := range []Strategy{{}, {Anonymity: "light", Bypass: true, Country: "DE", Route: true, Node_id: "0b9c1f2e-node_1"}} {
		if err := good.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v", good, err)
		}
	}
}

func Test_a_strategy_round_trips_and_a_bad_one_never_reaches_the_service(t *testing.T) {
	service := new_fake_service()
	client, _ := serve_pair(t, service)
	want := Strategy{Anonymity: "high", Country: "NL", Node_id: "node-2"}
	if err := client.Set_strategy(with_timeout(t), want); err != nil {
		t.Fatalf("Set_strategy() error = %v", err)
	}
	if got, err := client.Strategy(with_timeout(t)); err != nil || got != want {
		t.Fatalf("Strategy() = %+v, %v; want %+v", got, err, want)
	}
	err := client.Set_strategy(with_timeout(t), Strategy{Country: "Netherlands"})
	var remote *Remote_error
	if !errors.As(err, &remote) || remote.Code != Error_bad_request || service.strategy != want {
		t.Fatalf("a bad strategy: err %v, service holds %+v; want bad_request and nothing changed", err, service.strategy)
	}
}

func Test_a_server_that_cannot_be_used_is_reported_as_such(t *testing.T) {
	service := new_fake_service()
	service.set_strategy_err = Err_no_server_in_country
	client, _ := serve_pair(t, service)
	if err := client.Set_strategy(with_timeout(t), Strategy{Country: "JP"}); !errors.Is(err, Remote_no_server_in_country) {
		t.Fatalf("Set_strategy() error = %v, want no_server_in_country", err)
	}
	service.servers = Servers_result{Servers: []Server{{Node_id: "node-2", Name: "Germany 1", Available: true}}}
	if got, err := client.Servers(with_timeout(t)); err != nil || len(got.Servers) != 1 || got.Servers[0].Name != "Germany 1" {
		t.Fatalf("Servers() = %+v, %v", got, err)
	}
}
