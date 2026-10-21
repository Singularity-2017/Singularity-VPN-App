package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func Test_a_recommendation_request_asks_for_exactly_the_goals_that_are_on(t *testing.T) {
	cases := []struct {
		request Recommend_request
		want    string
	}{
		{Recommend_request{}, ""},
		{Recommend_request{Client_id: "c-1"}, ""}, // no goal measures it
		{Recommend_request{Bypass: true}, "goal=bypass"},
		{Recommend_request{Anonymity: "high", Country: "DE"}, "anonymity=high&country=DE&goal=anonymity&goal=geo"},
		{Recommend_request{Route: true, Client_id: "c-1"}, "client_id=c-1&goal=route"},
		{Recommend_request{Node_id: "node-2", Route: true}, "goal=route&node_id=node-2"},
		{Recommend_request{Game: "pubg", Bypass: true}, "game=pubg&goal=bypass"},
	}
	for _, c := range cases {
		if got := c.request.query().Encode(); got != c.want {
			t.Errorf("query(%+v) = %q, want %q", c.request, got, c.want)
		}
	}
}

func Test_recommend_sends_the_goals_and_reads_how_they_were_applied(t *testing.T) {
	var asked url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query()
		write_success(w, `{"node_id":"node-2","protocol":"wireguard","reason":"lowest_rtt",
			"goals":[{"goal":"anonymity","applied":false,"why":"not_supported"},{"goal":"route","applied":true,"compared":2}]}`)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Recommend(context.Background(), "tok", Recommend_request{Anonymity: "light", Route: true, Client_id: "c-1"})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if asked.Get("client_id") != "c-1" || len(asked["goal"]) != 2 || asked.Get("anonymity") != "light" {
		t.Fatalf("the request carried %v; want both goals, the level and the client", asked)
	}
	if got.Reason != "lowest_rtt" || len(got.Goals) != 2 || got.Goals[0].Why != "not_supported" || !got.Goals[1].Applied || got.Goals[1].Compared != 2 {
		t.Fatalf("Recommend = %+v; want the reason and both outcomes as sent", got)
	}
}

func Test_a_recommendation_from_a_control_plane_without_goals_is_still_usable(t *testing.T) {
	got, err := serve_data(t, `{"node_id":"node-1","protocol":"wireguard"}`).Recommend(context.Background(), "tok", Recommend_request{Bypass: true})
	if err != nil || got.Reason != "" || got.Goals != nil {
		t.Fatalf("Recommend from an older control plane = %+v, %v; want usable, with no reason or goals invented", got, err)
	}
}

func Test_a_servers_host_is_read_when_the_control_plane_sends_one_and_empty_when_it_does_not(t *testing.T) {
	list, err := serve_data(t, `{"servers":[{"node_id":"node-2","host":"192.0.2.10","available":true},{"node_id":"node-3","available":true}]}`).
		Servers(context.Background(), "tok")
	if err != nil || len(list) != 2 || list[0].Host != "192.0.2.10" || list[1].Host != "" {
		t.Fatalf("Servers = %+v, %v; want the host of the first and none for the second", list, err)
	}
}

func Test_the_server_list_is_read_and_a_server_without_an_id_refused(t *testing.T) {
	list, err := serve_data(t, `{"servers":[{"node_id":"node-2","name":"Germany 1","country":"DE","available":true},{"node_id":"node-3","available":false}]}`).
		Servers(context.Background(), "tok")
	if err != nil || len(list) != 2 || list[0].Name != "Germany 1" || !list[0].Available || list[1].Available || list[1].Name != "" {
		t.Fatalf("Servers = %+v, %v; want both, as sent", list, err)
	}
	if _, err := serve_data(t, `{"servers":[{"name":"nameless id"}]}`).Servers(context.Background(), "tok"); err == nil {
		t.Fatal("a server with no node_id was accepted; it could never be picked")
	}
}

func Test_the_reach_catalog_is_read_and_an_incomplete_target_refused(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		write_success(w, `{"country":"RU","targets":[
			{"key":"bank_ru","host":"sberbank.ru","port":443,"country":"RU","label":"Sber"},
			{"key":"web_youtube","host":"www.youtube.com","port":443,"label":"YouTube"}]}`)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{Base_url: server.URL, Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := client.Reach_targets(context.Background(), "tok")
	if err != nil || path != "/api/v1/reach/targets" {
		t.Fatalf("Reach_targets = %+v, %v (asked %q)", catalog, err, path)
	}
	if catalog.Country != "RU" || len(catalog.Targets) != 2 ||
		catalog.Targets[0] != (Reach_target{Key: "bank_ru", Host: "sberbank.ru", Port: 443, Country: "RU", Label: "Sber"}) ||
		catalog.Targets[1].Country != "" || catalog.Targets[1].Host != "www.youtube.com" {
		t.Fatalf("Reach_targets = %+v; want both targets, as sent", catalog)
	}

	unknown, err := serve_data(t, `{"targets":[{"key":"web_youtube","host":"www.youtube.com","port":443}]}`).Reach_targets(context.Background(), "tok")
	if err != nil || unknown.Country != "" {
		t.Fatalf("a catalog without a country = %+v, %v; want usable, with no country invented", unknown, err)
	}

	for _, body := range []string{
		`{"targets":[{"host":"a.example","port":443}]}`,
		`{"targets":[{"key":"a","port":443}]}`,
		`{"targets":[{"key":"a","host":"a.example"}]}`,
		`{"targets":[{"key":"a","host":"a.example","port":0}]}`,
		`{"targets":[{"key":"a","host":"a.example","port":70000}]}`,
		`{"targets":[{"key":"ok","host":"b.example","port":443},{"key":"a","host":"","port":443}]}`,
	} {
		if _, err := serve_data(t, body).Reach_targets(context.Background(), "tok"); !errors.Is(err, Err_incomplete_response) {
			t.Errorf("Reach_targets(%s) error = %v; want an incomplete response", body, err)
		}
	}
}
