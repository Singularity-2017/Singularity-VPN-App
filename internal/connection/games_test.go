package connection

import (
	"testing"

	"singularity-vpn-app/internal/state"
)

func Test_what_the_tunnel_carries(t *testing.T) {
	cases := []struct {
		name          string
		local         state.State
		games         int
		driver_banned bool
	}{
		{"nothing chosen", state.State{}, 0, false},
		{"a game alone", state.State{Games: []string{"pubg"}}, 1, false},
		{"a game and anonymity", state.State{Games: []string{"pubg"}, Strategy: state.Strategy{Anonymity: "high"}}, 0, false},
		{"a game and getting past blocks", state.State{Games: []string{"pubg"}, Strategy: state.Strategy{Bypass: true}}, 0, false},
		{"a game and a fast route", state.State{Games: []string{"pubg"}, Strategy: state.Strategy{Route: true}}, 1, false},
		{"a touchy game", state.State{Games: []string{"pubg", "cod"}}, 0, true},
		{"a game the catalog no longer has", state.State{Games: []string{"gone"}}, 0, false},
	}
	for _, c := range cases {
		split := split_for(c.local)
		if len(split.Games) != c.games || split.Driver_forbidden != c.driver_banned {
			t.Errorf("%s: split = %+v; want %d games, driver forbidden %v", c.name, split, c.games, c.driver_banned)
		}
	}
}
