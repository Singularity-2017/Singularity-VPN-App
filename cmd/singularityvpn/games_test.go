//go:build windows

package main

import (
	"reflect"
	"testing"
	"time"

	"singularity-vpn-app/internal/games"
	"singularity-vpn-app/internal/ipc"
)

func Test_game_cards_keep_catalog_order_and_mark_the_selected(t *testing.T) {
	catalog := games.Catalog{
		{Id: "pubg", Name: "PUBG", Images: []string{"TslGame.exe"}, Offered: true, Launch: "steam://rungameid/578080",
			Regions: []games.Region{{Id: "ru", Name: "RU", Target: "pubg_ru"}}},
		{Id: "cod", Name: "Call of Duty", Touchy_anticheat: true, Offered: true},
		{Id: "withdrawn", Name: "Withdrawn", Images: []string{"w.exe"}},
		{Id: "bare", Name: "Bare", Offered: true},
	}
	cards := game_cards(catalog, []string{"cod", "gone", "withdrawn"}, nil, nil)
	want := []Game_card{
		{Id: "pubg", Name: "PUBG", Regions: []Region{{Id: "ru", Name: "RU"}}, Launchable: true},
		{Id: "cod", Name: "Call of Duty", On: true, Whole_machine: true, Touchy: true, Regions: []Region{}},
		{Id: "bare", Name: "Bare", Whole_machine: true, Regions: []Region{}},
	}
	// Neither an unknown selected id nor a game no longer offered is drawn.
	if !reflect.DeepEqual(cards, want) {
		t.Errorf("got %+v, want %+v", cards, want)
	}
}

func Test_the_embedded_catalog_gives_cards(t *testing.T) {
	catalog, err := games.Load_catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(game_cards(catalog, nil, nil, nil)) == 0 {
		t.Fatal("the embedded catalog gave no cards")
	}
}

func Test_a_game_with_a_measured_ping_carries_it_and_others_do_not(t *testing.T) {
	catalog := games.Catalog{{Id: "pubg", Name: "PUBG", Images: []string{"TslGame.exe"}, Offered: true}, {Id: "cs2", Name: "CS2", Images: []string{"cs2.exe"}, Offered: true}}
	at := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	cards := game_cards(catalog, []string{"pubg"}, []ipc.Game_ping{{Game: "pubg", Rtt_ms: 38.5, At: at}}, nil)
	if cards[0].Ping_ms == nil || *cards[0].Ping_ms != 38.5 || !cards[0].Ping_at.Equal(at) {
		t.Errorf("pubg card %+v", cards[0])
	}
	if cards[1].Ping_ms != nil || cards[1].Ping_at != nil {
		t.Errorf("a game never measured carries a ping: %+v", cards[1])
	}
}

func Test_a_card_offers_the_match_server_and_shows_the_tally_only_once_checked(t *testing.T) {
	catalog := games.Catalog{
		{Id: "pubg", Name: "PUBG", Images: []string{"TslGame.exe"}, Offered: true, Server_shown: "where"},
		{Id: "cs2", Name: "CS2", Images: []string{"cs2.exe"}, Offered: true},
	}
	cards := game_cards(catalog, []string{"pubg", "cs2"}, nil, []ipc.Match_server{
		{Game: "pubg", Server: "203.0.113.10:7086", Packets: 900, Runner_up: 40, Total: 1000},
		{Game: "cs2", Server: "203.0.113.20:27015", Packets: 500, Total: 600},
	})
	if o := cards[0].Server_offer; o == nil || o.Server != "203.0.113.10:7086" || o.Where != "where" || o.Packets != 900 {
		t.Errorf("pubg offer %+v", o)
	}
	if cards[0].Detection_confirmed != nil || cards[0].Detection_right != nil {
		t.Errorf("a tally shown before any check: %+v", cards[0])
	}
	if cards[1].Server_offer != nil {
		t.Errorf("a game that shows no server was asked: %+v", cards[1].Server_offer)
	}
	cards = game_cards(catalog, []string{"pubg"}, nil, []ipc.Match_server{{Game: "pubg", Right: 2, Confirmed: 3}})
	if cards[0].Server_offer != nil || cards[0].Detection_right == nil || *cards[0].Detection_right != 2 || *cards[0].Detection_confirmed != 3 {
		t.Errorf("pubg card %+v", cards[0])
	}
}
