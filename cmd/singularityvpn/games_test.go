//go:build windows

package main

import (
	"testing"
	"time"

	"singularity-vpn-app/internal/games"
	"singularity-vpn-app/internal/ipc"
)

func Test_game_cards_keep_catalog_order_and_mark_the_selected(t *testing.T) {
	catalog := games.Catalog{
		{Id: "pubg", Name: "PUBG", Images: []string{"TslGame.exe"}},
		{Id: "cod", Name: "Call of Duty", Touchy_anticheat: true},
		{Id: "bare", Name: "Bare"},
	}
	cards := game_cards(catalog, []string{"cod", "gone"}, nil)
	if len(cards) != 3 {
		t.Fatalf("got %d cards, want 3 (an unknown selected id is not drawn)", len(cards))
	}
	want := []Game_card{
		{Id: "pubg", Name: "PUBG"},
		{Id: "cod", Name: "Call of Duty", On: true, Whole_machine: true, Touchy: true},
		{Id: "bare", Name: "Bare", Whole_machine: true},
	}
	for i := range want {
		if cards[i] != want[i] {
			t.Errorf("card %d: got %+v, want %+v", i, cards[i], want[i])
		}
	}
}

func Test_the_embedded_catalog_gives_cards(t *testing.T) {
	catalog, err := games.Load_catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(game_cards(catalog, nil, nil)) == 0 {
		t.Fatal("the embedded catalog gave no cards")
	}
}

func Test_a_game_with_a_measured_ping_carries_it_and_others_do_not(t *testing.T) {
	catalog := games.Catalog{{Id: "pubg", Name: "PUBG", Images: []string{"TslGame.exe"}}, {Id: "cs2", Name: "CS2", Images: []string{"cs2.exe"}}}
	at := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	cards := game_cards(catalog, []string{"pubg"}, []ipc.Game_ping{{Game: "pubg", Rtt_ms: 38.5, At: at}})
	if cards[0].Ping_ms == nil || *cards[0].Ping_ms != 38.5 || !cards[0].Ping_at.Equal(at) {
		t.Errorf("pubg card %+v", cards[0])
	}
	if cards[1].Ping_ms != nil || cards[1].Ping_at != nil {
		t.Errorf("a game never measured carries a ping: %+v", cards[1])
	}
}
