package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
)

func Test_a_nodes_game_round_trip_is_read_once_a_minute(t *testing.T) {
	var n node_game_rtts
	reads := 0
	fetch := func(context.Context) ([]api.Server, error) {
		reads++
		return []api.Server{{Node_id: "de-1", Game_rtt_ms: map[string]float64{"pubg": 2.2}}, {Node_id: "se-1"}}, nil
	}
	now := time.Date(2026, 10, 21, 5, 0, 0, 0, time.UTC)
	if ms, ok := n.lookup(context.Background(), now, fetch, "de-1", "pubg"); !ok || ms != 2.2 {
		t.Fatalf("got %v %v", ms, ok)
	}
	if _, ok := n.lookup(context.Background(), now.Add(30*time.Second), fetch, "de-1", "cs2"); ok {
		t.Fatal("a game the node has no measurement for got one")
	}
	if _, ok := n.lookup(context.Background(), now, fetch, "se-1", "pubg"); ok {
		t.Fatal("a node without measurements got one")
	}
	if reads != 1 {
		t.Fatalf("read the list %d times within a minute", reads)
	}
	n.lookup(context.Background(), now.Add(time.Minute), fetch, "de-1", "pubg")
	if reads != 2 {
		t.Fatalf("read the list %d times after a minute", reads)
	}
}

func Test_a_failed_server_list_gives_no_round_trip(t *testing.T) {
	var n node_game_rtts
	fetch := func(context.Context) ([]api.Server, error) { return nil, errors.New("offline") }
	if _, ok := n.lookup(context.Background(), time.Now(), fetch, "de-1", "pubg"); ok {
		t.Fatal("a round trip came from a failed read")
	}
}
