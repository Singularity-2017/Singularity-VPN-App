package ipc

import (
	"errors"
	"reflect"
	"testing"
	"time"
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

func Test_server_latencies_round_trip_by_node_id_alone(t *testing.T) {
	service := new_fake_service()
	client, _ := serve_pair(t, service)

	want := Ping_servers_result{Pings: []Server_ping{{Node_id: "node-1", Rtt_ms: 12.5}, {Node_id: "node-2", Rtt_ms: 0.4}}}
	service.ping_servers = want
	got, err := client.Ping_servers(with_timeout(t))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Ping_servers() = %+v, %v; want %+v", got, err, want)
	}

	service.ping_servers_err = Err_not_signed_in
	if _, err := client.Ping_servers(with_timeout(t)); !errors.Is(err, Remote_not_signed_in) {
		t.Fatalf("Ping_servers() error = %v, want the service's refusal passed on", err)
	}
}

func Test_an_analysis_round_trips_and_is_absent_until_there_is_one(t *testing.T) {
	service := new_fake_service()
	client, _ := serve_pair(t, service)

	if got, err := client.Analysis(with_timeout(t)); err != nil || got.Present || got.Result != nil {
		t.Fatalf("Analysis() before any run = %+v, %v; want nothing present", got, err)
	}

	want := Analysis_result{
		Measured_at: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		Country:     "RU",
		Control:     "ok",
		Checks: []Reach_check{
			{Key: "bank_ru", Label: "Bank", Host: "bank.example", Control: true, Dns: "open", Tcp: "open", Tls: "open", Verdict: "open"},
			{Key: "web_youtube", Label: "YouTube", Host: "www.youtube.com", Dns: "open", Tcp: "open", Tls: "blocked",
				Failure: "reset", Verdict: "blocked", Method: "tls"},
		},
	}
	service.analyze_result = want
	got, err := client.Analyze(with_timeout(t))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Analyze() = %+v, %v; want %+v", got, err, want)
	}

	service.analysis = Last_analysis{Present: true, Result: &want}
	last, err := client.Analysis(with_timeout(t))
	if err != nil || !last.Present || last.Result == nil || !reflect.DeepEqual(*last.Result, want) {
		t.Fatalf("Analysis() = %+v, %v; want the result as kept", last, err)
	}
}

func Test_an_analysis_refused_because_the_tunnel_is_up_says_so(t *testing.T) {
	service := new_fake_service()
	service.analyze_err = Err_tunnel_up
	client, _ := serve_pair(t, service)
	if _, err := client.Analyze(with_timeout(t)); !errors.Is(err, Remote_tunnel_up) {
		t.Fatalf("Analyze() error = %v, want tunnel_up", err)
	}
}

func Test_a_game_list_the_service_would_not_read_is_refused(t *testing.T) {
	for _, bad := range []Games{{Games: []string{""}}, {Games: []string{"PUBG"}}, {Games: []string{"a b"}}} {
		if bad.Validate() == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	for _, good := range []Games{{}, {Games: []string{}}, {Games: []string{"pubg", "cs2", "dota_2"}}} {
		if err := good.Validate(); err != nil {
			t.Errorf("%+v: %v", good, err)
		}
	}
}

func Test_a_tunnel_mode_other_than_the_two_is_refused(t *testing.T) {
	for _, bad := range []string{"", "full", "Antizapret", "VPN", "games", "antizapret "} {
		if (Tunnel_mode{Mode: bad}).Validate() == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, good := range []string{"vpn", "antizapret"} {
		if err := (Tunnel_mode{Mode: good}).Validate(); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func Test_a_tunnel_mode_travels_to_the_service_and_back(t *testing.T) {
	service := new_fake_service()
	client, _ := serve_pair(t, service)
	if err := client.Set_tunnel_mode(with_timeout(t), Tunnel_mode{Mode: Tunnel_antizapret}); err != nil {
		t.Fatalf("Set_tunnel_mode: %v", err)
	}
	got, err := client.Tunnel_mode(with_timeout(t))
	if err != nil || got.Mode != Tunnel_antizapret {
		t.Fatalf("Tunnel_mode() = %+v, %v; want antizapret", got, err)
	}
	if err := client.Set_tunnel_mode(with_timeout(t), Tunnel_mode{Mode: "full"}); err == nil {
		t.Fatal("a mode the service would not read was accepted")
	}
}
