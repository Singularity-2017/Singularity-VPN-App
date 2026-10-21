// Package games is the catalog of games the application can optimize and
// the rule that decides how their traffic is carried: per process through
// the packet driver, or the whole machine through the tunnel.
//
// The catalog is data. A game whose anti-cheat reacts to a loaded packet
// driver is a flag in catalog.json, not a branch in code. It is embedded
// for now; later it comes from the control plane.
package games

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// Game is one entry of the catalog.
type Game struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	// Images are the executable names of the game's whole process group --
	// the client, its launcher helpers and its anti-cheat services, which
	// must leave through the same address or the anti-cheat kicks the
	// player. Matched case-insensitively. Empty for a game that is never
	// routed per process.
	Images []string `json:"images"`
	// Touchy_anticheat is set for a game whose anti-cheat reacts to the
	// packet driver merely being loaded. While such a game is selected the
	// driver must not exist on the machine.
	Touchy_anticheat bool `json:"touchy_anticheat"`
	// Offered is set for a game the window shows. The others stay in the
	// catalog so that a selection saved before they were withdrawn still
	// routes, but nobody can pick them.
	Offered bool `json:"offered"`
	// Launch is the address that starts the game, a steam:// one; empty
	// for a game the application does not start.
	Launch string `json:"launch"`
	// Regions are the game's server regions a person can boost toward, in
	// display order. Empty for a game boosted toward wherever it plays.
	Regions []Region `json:"regions"`
	// Server_shown tells the player where the game shows the address of
	// the server it is playing on, in the window's language; empty for a
	// game that does not show it. Only such a game is asked to confirm
	// the detected match server.
	Server_shown string `json:"server_shown"`
}

// Region is one of a game's server regions.
type Region struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	// Target is the key the control plane knows the region by: what goes
	// into /vpn/recommend?game= and what game_rtt_ms is keyed by. A
	// region the control plane does not measure yet gets an ordinary
	// choice and no node-side round trip, which is the honest answer.
	Target string `json:"target"`
	// Direct marks a region played without the tunnel: the game's
	// traffic and its DNS go the machine's own way. The game picks its
	// server from the country it sees, so a region at home is reached
	// only from home -- through a node abroad it lands on that node's
	// region instead.
	Direct bool `json:"direct,omitempty"`
}

// Catalog is every game the application knows, in display order.
type Catalog []Game

//go:embed catalog.json
var embedded_catalog []byte

// Load_catalog parses the embedded catalog and refuses one that would make
// the routing rule ambiguous: an empty or repeated id, or an image name
// claimed by two games.
func Load_catalog() (Catalog, error) {
	return parse_catalog(embedded_catalog)
}

func parse_catalog(data []byte) (Catalog, error) {
	var catalog Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("games: parsing the catalog: %w", err)
	}
	ids := map[string]bool{}
	images := map[string]string{}
	for _, game := range catalog {
		if game.Id == "" {
			return nil, fmt.Errorf("games: a catalog entry has no id")
		}
		if ids[game.Id] {
			return nil, fmt.Errorf("games: id %q appears twice in the catalog", game.Id)
		}
		ids[game.Id] = true
		if game.Launch != "" && !strings.HasPrefix(game.Launch, "steam://") {
			return nil, fmt.Errorf("games: %q launches through %q, and only steam:// addresses are opened", game.Id, game.Launch)
		}
		regions := map[string]bool{}
		for _, region := range game.Regions {
			if !is_key(region.Id) || !is_key(region.Target) || regions[region.Id] {
				return nil, fmt.Errorf("games: %q has a region with an empty, repeated or malformed id or target", game.Id)
			}
			regions[region.Id] = true
		}
		for _, image := range game.Images {
			key := strings.ToLower(image)
			if owner, taken := images[key]; taken {
				return nil, fmt.Errorf("games: image %q belongs to both %q and %q", image, owner, game.Id)
			}
			images[key] = game.Id
		}
	}
	return catalog, nil
}

// is_key reports whether s can be a region id or a control plane game key:
// the control plane takes [a-z0-9_], at most 32 long.
func is_key(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// Region returns the game's region with the given id.
func (g Game) Region(id string) (Region, bool) {
	for _, region := range g.Regions {
		if region.Id == id {
			return region, true
		}
	}
	return Region{}, false
}

// Target is the key the control plane knows the game by when boosted
// toward region: the region's own key, or the game's id when no region is
// chosen or the game has no such region.
func (c Catalog) Target(id, region string) string {
	if game, ok := c.Find(id); ok {
		if r, ok := game.Region(region); ok {
			return r.Target
		}
	}
	return id
}

// Find returns the game with the given id.
func (c Catalog) Find(id string) (Game, bool) {
	for _, game := range c {
		if game.Id == id {
			return game, true
		}
	}
	return Game{}, false
}

// By_image returns the game whose process group includes the executable
// named image, compared case-insensitively. Load_catalog has already
// refused an image claimed by two games, so the answer is unique.
func (c Catalog) By_image(image string) (Game, bool) {
	for _, game := range c {
		for _, candidate := range game.Images {
			if strings.EqualFold(candidate, image) {
				return game, true
			}
		}
	}
	return Game{}, false
}

// Plan is how the traffic of the selected games is carried.
type Plan struct {
	// Driver says whether the packet driver may be loaded. False means it
	// must not exist: not loaded, and removed if it is.
	Driver bool
	// Whole_machine says the tunnel carries all of the machine's traffic,
	// because at least one selected game cannot be routed per process.
	Whole_machine bool
	// Per_process are the games routed per process; empty when
	// Whole_machine is set or nothing is selected.
	Per_process []Game
}

// Plan_for decides how the selected games are carried. Nothing selected
// needs no driver. A selected game with a touchy anti-cheat keeps the
// driver off for every game and puts the whole machine through the tunnel:
// the driver cannot be loaded for one game and hidden from another. An id
// the catalog does not know is an error -- a selection made against an
// older catalog must not silently lose a game.
func Plan_for(selected []string, catalog Catalog) (Plan, error) {
	chosen := make([]Game, 0, len(selected))
	touchy := false
	for _, id := range selected {
		game, ok := catalog.Find(id)
		if !ok {
			return Plan{}, fmt.Errorf("games: %q is not in the catalog", id)
		}
		chosen = append(chosen, game)
		touchy = touchy || game.Touchy_anticheat
	}
	switch {
	case len(chosen) == 0:
		return Plan{}, nil
	case touchy:
		return Plan{Whole_machine: true}, nil
	default:
		return Plan{Driver: true, Per_process: chosen}, nil
	}
}
