//go:build windows

package main

import (
	"testing"

	"singularity-vpn-app/internal/games"
)

func Test_game_cards_keep_catalog_order_and_mark_the_selected(t *testing.T) {
	catalog := games.Catalog{
		{Id: "pubg", Name: "PUBG", Images: []string{"TslGame.exe"}},
		{Id: "cod", Name: "Call of Duty", Touchy_anticheat: true},
		{Id: "bare", Name: "Bare"},
	}
	cards := game_cards(catalog, []string{"cod", "gone"})
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
	if len(game_cards(catalog, nil)) == 0 {
		t.Fatal("the embedded catalog gave no cards")
	}
}
