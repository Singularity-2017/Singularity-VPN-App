//go:build windows

package main

import (
	"context"
	"slices"
	"time"

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
	// Ping_ms is the last round trip measured to the server the game was
	// playing on, through the tunnel, and Ping_at when. Absent until one
	// was measured.
	Ping_ms *float64   `json:"ping_ms,omitempty"`
	Ping_at *time.Time `json:"ping_at,omitempty"`
	// Ping_first_mile_ms and Ping_node_ms are the two halves Ping_ms was
	// added up from, when the match server ignored echo: this machine to
	// the VPN server, and the VPN server to the game's region.
	Ping_first_mile_ms *float64 `json:"ping_first_mile_ms,omitempty"`
	Ping_node_ms       *float64 `json:"ping_node_ms,omitempty"`
}

// Games_view is the game list, or why it could not be read.
type Games_view struct {
	Games []Game_card `json:"games"`
	Error *Outcome    `json:"error,omitempty"`
}

// game_cards lays the catalog out in its own order and marks the selected
// games. An id the service holds that the catalog does not know is not
// drawn: there is nothing to call it.
func game_cards(catalog games.Catalog, selected []string, pings []ipc.Game_ping) []Game_card {
	cards := make([]Game_card, 0, len(catalog))
	for _, g := range catalog {
		card := Game_card{
			Id:            g.Id,
			Name:          g.Name,
			On:            slices.Contains(selected, g.Id),
			Whole_machine: g.Touchy_anticheat || len(g.Images) == 0,
			Touchy:        g.Touchy_anticheat,
		}
		for _, p := range pings {
			if p.Game == g.Id {
				card.Ping_ms, card.Ping_at = &p.Rtt_ms, &p.At
				card.Ping_first_mile_ms, card.Ping_node_ms = p.First_mile_ms, p.Node_ms
			}
		}
		cards = append(cards, card)
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
			view.Games = game_cards(catalog, result.Games, result.Pings)
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
