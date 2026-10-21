package games

import (
	"reflect"
	"testing"
)

func test_catalog(t *testing.T) Catalog {
	t.Helper()
	catalog, err := parse_catalog([]byte(`[
		{"id": "calm", "images": ["calm.exe", "calm_ac.exe"]},
		{"id": "other", "images": ["other.exe"]},
		{"id": "touchy", "touchy_anticheat": true}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func Test_the_embedded_catalog_loads(t *testing.T) {
	catalog, err := Load_catalog()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Find("pubg"); !ok {
		t.Fatal("pubg is missing from the catalog")
	}
	cod, ok := catalog.Find("cod")
	if !ok || !cod.Touchy_anticheat {
		t.Fatalf("cod = %+v, %v; want it present and touchy", cod, ok)
	}
}

func Test_only_pubg_is_offered_and_it_boosts_toward_ru_and_eu(t *testing.T) {
	catalog, err := Load_catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, game := range catalog {
		if game.Offered != (game.Id == "pubg") {
			t.Errorf("%s offered = %v", game.Id, game.Offered)
		}
	}
	// EU keeps the key the control plane already measures.
	if got := catalog.Target("pubg", "eu"); got != "pubg" {
		t.Errorf("Target(pubg, eu) = %q", got)
	}
	if got := catalog.Target("pubg", "ru"); got != "pubg_ru" {
		t.Errorf("Target(pubg, ru) = %q", got)
	}
	for _, region := range []string{"", "mars"} {
		if got := catalog.Target("pubg", region); got != "pubg" {
			t.Errorf("Target(pubg, %q) = %q; want the game's own id", region, got)
		}
	}
}

func Test_an_ambiguous_catalog_is_refused(t *testing.T) {
	cases := map[string]string{
		"empty id":     `[{"id": ""}]`,
		"repeated id":  `[{"id": "a"}, {"id": "a"}]`,
		"shared image": `[{"id": "a", "images": ["X.exe"]}, {"id": "b", "images": ["x.EXE"]}]`,
		"not json":     `{`,
		"launch":       `[{"id": "a", "launch": "cmd.exe /c calc"}]`,
		"region id":    `[{"id": "a", "regions": [{"id": "", "target": "a"}]}]`,
		"region twice": `[{"id": "a", "regions": [{"id": "eu", "target": "a"}, {"id": "eu", "target": "b"}]}]`,
		"target":       `[{"id": "a", "regions": [{"id": "eu", "target": "a&b"}]}]`,
	}
	for name, data := range cases {
		if _, err := parse_catalog([]byte(data)); err == nil {
			t.Errorf("%s: parse_catalog accepted it", name)
		}
	}
}

func Test_plan_for(t *testing.T) {
	catalog := test_catalog(t)
	calm, _ := catalog.Find("calm")
	other, _ := catalog.Find("other")
	cases := []struct {
		name     string
		selected []string
		want     Plan
	}{
		{"nothing selected needs no driver", nil, Plan{}},
		{"calm games go per process", []string{"calm", "other"}, Plan{Driver: true, Per_process: []Game{calm, other}}},
		{"a touchy game alone", []string{"touchy"}, Plan{Whole_machine: true}},
		{"a touchy game keeps the driver off for the others too", []string{"calm", "touchy"}, Plan{Whole_machine: true}},
	}
	for _, c := range cases {
		got, err := Plan_for(c.selected, catalog)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func Test_an_unknown_game_is_an_error(t *testing.T) {
	if _, err := Plan_for([]string{"calm", "gone"}, test_catalog(t)); err == nil {
		t.Fatal("Plan_for accepted a game the catalog does not know")
	}
}

func Test_a_process_is_matched_to_its_game_by_image(t *testing.T) {
	catalog, err := Load_catalog()
	if err != nil {
		t.Fatal(err)
	}
	if game, ok := catalog.By_image("tslgame.EXE"); !ok || game.Id != "pubg" {
		t.Fatalf("By_image(tslgame.EXE) = %+v, %v; want pubg", game, ok)
	}
	if _, ok := catalog.By_image("chrome.exe"); ok {
		t.Fatal("a browser was matched to a game")
	}
}
