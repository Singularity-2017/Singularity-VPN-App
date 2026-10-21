//go:build windows

package main

import (
	"context"
	"slices"

	"singularity-vpn-app/internal/games"
	"singularity-vpn-app/internal/ipc"
)

// Game_card is one game the window offers, and whether it is on.
type Game_card struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	On   bool   `json:"on"`
	// Whole_machine is set for a game that cannot be carried on its own:
	// its anti-cheat reacts to the packet driver, or the catalog names no
	// executables for it. While it is on the whole machine goes through
	// the tunnel.
	Whole_machine bool `json:"whole_machine"`
	// Touchy is why Whole_machine is set, when it is the anti-cheat.
	Touchy bool `json:"touchy"`
}

// Games_view is the game list, or why it could not be read.
type Games_view struct {
	Games []Game_card `json:"games"`
	Error *Outcome    `json:"error,omitempty"`
}

// game_cards lays the catalog out in its own order and marks the selected
// games. An id the service holds that the catalog does not know is not
// drawn: there is nothing to call it.
func game_cards(catalog games.Catalog, selected []string) []Game_card {
	cards := make([]Game_card, 0, len(catalog))
	for _, g := range catalog {
		cards = append(cards, Game_card{
			Id:            g.Id,
			Name:          g.Name,
			On:            slices.Contains(selected, g.Id),
			Whole_machine: g.Touchy_anticheat || len(g.Images) == 0,
			Touchy:        g.Touchy_anticheat,
		})
	}
	return cards
}

// Games is the catalog the window and the service were built with, and
// which of its games the service carries through the tunnel.
func (a *App) Games() Games_view {
	view := Games_view{Games: []Game_card{}}
	catalog, err := games.Load_catalog()
	if err != nil {
		view.Error = outcome_of(err)
		return view
	}
	outcome := a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		result, err := c.Games(ctx)
		if err == nil {
			view.Games = game_cards(catalog, result.Games)
		}
		return err
	})
	if outcome.Code != "" {
		view.Error = &outcome
	}
	return view
}

// Set_games records which games go through the tunnel. If a tunnel is up
// the service rebuilds it on the same server, which is why this can take as
// long as a connect.
func (a *App) Set_games(ids []string) Outcome {
	if ids == nil {
		ids = []string{}
	}
	return a.act(func(ctx context.Context, c *ipc.Client) error {
		return c.Set_games(ctx, ipc.Games{Games: ids})
	})
}
