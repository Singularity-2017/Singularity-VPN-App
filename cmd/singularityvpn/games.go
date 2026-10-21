//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/windows"

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
	// Regions are the server regions the game can be boosted toward, in
	// display order; Launchable says the window can start it.
	Regions    []Region `json:"regions"`
	Launchable bool     `json:"launchable"`
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
	// Server_offer is the match server detected for the current or last
	// match, for the player to check against what the game shows; absent
	// when nothing waits for an answer, or the game shows no server.
	Server_offer *Server_offer `json:"server_offer,omitempty"`
	// Detection_right of Detection_confirmed matches the player checked
	// had the server detected right. Absent until one was checked.
	Detection_right     *int `json:"detection_right,omitempty"`
	Detection_confirmed *int `json:"detection_confirmed,omitempty"`
}

// Server_offer is a detected match server put to the player: Server, the
// address and port, got Packets of Total packets, the next address
// Runner_up; Where says where the game shows its own.
type Server_offer struct {
	Server    string `json:"server"`
	Packets   int    `json:"packets"`
	Runner_up int    `json:"runner_up"`
	Total     int    `json:"total"`
	Where     string `json:"where"`
}

// Region is one server region of a game, as the window names it.
type Region struct {
	Id     string `json:"id"`
	Name   string `json:"name"`
	Direct bool   `json:"direct,omitempty"`
}

// Games_view is the game list, or why it could not be read.
type Games_view struct {
	Games []Game_card `json:"games"`
	// Region is the region the selected game is boosted toward; empty for
	// wherever it plays.
	Region string `json:"region,omitempty"`
	// In_match is whether a game is in a match: changing the server or
	// the games now would end it, so the window asks first.
	In_match bool     `json:"in_match"`
	Error    *Outcome `json:"error,omitempty"`
}

// game_cards lays the offered games out in the catalog's order and marks
// the selected ones. An id the service holds that the catalog does not know,
// or no longer offers, is not drawn: there is nothing to pick it with.
func game_cards(catalog games.Catalog, selected []string, pings []ipc.Game_ping, servers []ipc.Match_server) []Game_card {
	cards := make([]Game_card, 0, len(catalog))
	for _, g := range catalog {
		if !g.Offered {
			continue
		}
		card := Game_card{
			Id:            g.Id,
			Name:          g.Name,
			On:            slices.Contains(selected, g.Id),
			Whole_machine: g.Touchy_anticheat || len(g.Images) == 0,
			Touchy:        g.Touchy_anticheat,
			Regions:       []Region{},
			Launchable:    g.Launch != "",
		}
		for _, r := range g.Regions {
			card.Regions = append(card.Regions, Region{Id: r.Id, Name: r.Name, Direct: r.Direct})
		}
		for _, p := range pings {
			if p.Game == g.Id {
				card.Ping_ms, card.Ping_at = &p.Rtt_ms, &p.At
				card.Ping_first_mile_ms, card.Ping_node_ms = p.First_mile_ms, p.Node_ms
			}
		}
		for _, m := range servers {
			if m.Game != g.Id {
				continue
			}
			if m.Server != "" && g.Server_shown != "" {
				card.Server_offer = &Server_offer{Server: m.Server, Packets: m.Packets, Runner_up: m.Runner_up, Total: m.Total, Where: g.Server_shown}
			}
			if m.Confirmed > 0 {
				right, confirmed := m.Right, m.Confirmed
				card.Detection_right, card.Detection_confirmed = &right, &confirmed
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
			view.Games = game_cards(catalog, result.Games, result.Pings, result.Match_servers)
			view.Region = result.Region
			view.In_match = result.In_match
		}
		return err
	})
	if outcome.Code != "" {
		view.Error = &outcome
	}
	return view
}

// Set_games records which games go through the tunnel and the region the
// one selected game is boosted toward. If a tunnel is up the service
// rebuilds it, and moves it when the region asks for another server, which
// is why this can take as long as a connect.
func (a *App) Set_games(ids []string, region string) Outcome {
	if ids == nil {
		ids = []string{}
	}
	return a.act(func(ctx context.Context, c *ipc.Client) error {
		return c.Set_games(ctx, ipc.Games{Games: ids, Region: region})
	})
}

// Answer_match_server records the player's answer about the match server
// offered on a game's card: answer is "yes", "no" or "dismiss", server the
// one the card showed, shown the address the game shows, on a "no", if
// they typed it.
func (a *App) Answer_match_server(game, server, answer, shown string) Outcome {
	return a.act_quietly(func(ctx context.Context, c *ipc.Client) error {
		return c.Answer_match_server(ctx, ipc.Match_server_answer{Game: game, Server: server, Answer: answer, Shown: shown})
	})
}

// Launch_game starts a game the catalog says how to start, through Steam,
// as the person signed in: the window runs as them and the service does
// not, which is why this is the window's to do.
func (a *App) Launch_game(id string) Outcome {
	catalog, err := games.Load_catalog()
	if err != nil {
		return *outcome_of(err)
	}
	game, ok := catalog.Find(id)
	if !ok || game.Launch == "" {
		return *outcome_of(errors.New("the application does not know how to start this game"))
	}
	if err := windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(game.Launch), nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return Outcome{Code: code_launch_failed, Message: err.Error()}
	}
	return Outcome{}
}

// Gamer_default is whether the window opens in gamer mode before the
// person has chosen: whether the setup that installed it came from the
// gamers' page, which marks the installation directory.
func (a *App) Gamer_default() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(filepath.Dir(self), gamer_mark))
	return err == nil
}

// code_launch_failed is the outcome of a game that Steam did not start:
// most often Steam is not installed.
const code_launch_failed = "launch_failed"

// gamer_mark is the file the setup leaves in the installation directory
// when it came from the gamers' page.
const gamer_mark = "gamer"
