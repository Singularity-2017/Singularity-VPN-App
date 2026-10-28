package control_address

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const compiled = "http://198.51.100.1/vpn"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func at(hour int) time.Time {
	return time.Date(2026, 10, 28, hour, 0, 0, 0, time.UTC)
}

func Test_nothing_stored_means_the_compiled_address(t *testing.T) {
	s := Open(t.TempDir(), compiled, quiet)
	if got := s.Current(); !reflect.DeepEqual(got, []string{compiled}) {
		t.Fatalf("Current = %v, want the compiled address", got)
	}
}

func Test_an_offered_list_replaces_the_compiled_address_and_survives_a_restart(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, compiled, quiet)
	list := []string{"http://203.0.113.7/vpn", "https://192.0.2.4/vpn"}
	changed, err := s.Offer(list, at(10))
	if err != nil || !changed {
		t.Fatalf("Offer = %v, %v; want changed", changed, err)
	}
	if got := s.Current(); !reflect.DeepEqual(got, list) {
		t.Fatalf("Current = %v, want %v", got, list)
	}
	again := Open(dir, compiled, quiet)
	if got := again.Current(); !reflect.DeepEqual(got, list) {
		t.Fatalf("after restart Current = %v, want %v", got, list)
	}
}

// A replayed older manifest must not bring back an address a newer one
// dropped: the dropped address may belong to someone else by now.
func Test_an_older_or_equally_old_statement_changes_nothing(t *testing.T) {
	s := Open(t.TempDir(), compiled, quiet)
	current := []string{"http://203.0.113.7/vpn"}
	if _, err := s.Offer(current, at(10)); err != nil {
		t.Fatal(err)
	}
	for _, when := range []time.Time{at(9), at(10)} {
		changed, err := s.Offer([]string{"http://192.0.2.9/vpn"}, when)
		if err != nil || changed {
			t.Fatalf("Offer at %v = %v, %v; want ignored", when, changed, err)
		}
	}
	if got := s.Current(); !reflect.DeepEqual(got, current) {
		t.Fatalf("Current = %v, want %v", got, current)
	}
}

func Test_an_empty_list_is_not_no_control_plane(t *testing.T) {
	s := Open(t.TempDir(), compiled, quiet)
	changed, err := s.Offer(nil, at(10))
	if err != nil || changed {
		t.Fatalf("Offer(nil) = %v, %v; want ignored", changed, err)
	}
	if got := s.Current(); !reflect.DeepEqual(got, []string{compiled}) {
		t.Fatalf("Current = %v", got)
	}
}

func Test_the_same_list_newer_is_not_a_change_but_raises_the_bar(t *testing.T) {
	s := Open(t.TempDir(), compiled, quiet)
	list := []string{"http://203.0.113.7/vpn"}
	s.Offer(list, at(10))
	changed, err := s.Offer([]string{"http://203.0.113.7/vpn/"}, at(11))
	if err != nil || changed {
		t.Fatalf("same list = %v, %v; want unchanged", changed, err)
	}
	// at(10) was newer than the first statement but is not newer than
	// the second one, which the store must have remembered.
	if changed, _ := s.Offer([]string{"http://192.0.2.9/vpn"}, at(10)); changed {
		t.Fatal("a statement older than the last accepted one was taken")
	}
}

func Test_a_malformed_list_is_refused_whole(t *testing.T) {
	bad := [][]string{
		{"http://203.0.113.7/vpn", "ftp://192.0.2.1"},
		{"203.0.113.7"},
		{"http://203.0.113.7/vpn?x=1"},
		{"http://203.0.113.7/vpn#f"},
		{"http://user@203.0.113.7/vpn"},
		{"a", "b", "c", "d", "e"},
	}
	for _, list := range bad {
		s := Open(t.TempDir(), compiled, quiet)
		if changed, err := s.Offer(list, at(10)); err == nil || changed {
			t.Errorf("Offer(%v) = %v, %v; want refused", list, changed, err)
		}
		if got := s.Current(); !reflect.DeepEqual(got, []string{compiled}) {
			t.Errorf("after refusing %v Current = %v", list, got)
		}
	}
}

func Test_duplicates_and_trailing_slashes_collapse(t *testing.T) {
	s := Open(t.TempDir(), compiled, quiet)
	s.Offer([]string{"http://203.0.113.7/vpn/", "http://203.0.113.7/vpn"}, at(10))
	if got := s.Current(); !reflect.DeepEqual(got, []string{"http://203.0.113.7/vpn"}) {
		t.Fatalf("Current = %v", got)
	}
}

// A file that does not parse means nothing is stored; the store must
// keep working and the next offer must repair the file.
func Test_an_unreadable_file_means_nothing_stored(t *testing.T) {
	for _, content := range []string{"{not json", `{"addresses":["ftp://x"],"released_at":"2026-10-28T10:00:00Z"}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, File_name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		s := Open(dir, compiled, quiet)
		if got := s.Current(); !reflect.DeepEqual(got, []string{compiled}) {
			t.Fatalf("%q: Current = %v", content, got)
		}
		if changed, err := s.Offer([]string{"http://203.0.113.7/vpn"}, at(1)); err != nil || !changed {
			t.Fatalf("%q: Offer = %v, %v", content, changed, err)
		}
	}
}

func Test_a_change_is_announced_once(t *testing.T) {
	s := Open(t.TempDir(), compiled, quiet)
	var heard [][]string
	s.On_change(func(list []string) { heard = append(heard, list) })
	s.Offer([]string{"http://203.0.113.7/vpn"}, at(10))
	s.Offer([]string{"http://203.0.113.7/vpn"}, at(11))
	s.Offer([]string{"http://192.0.2.9/vpn"}, at(9))
	if len(heard) != 1 || heard[0][0] != "http://203.0.113.7/vpn" {
		t.Fatalf("heard %v, want one announcement", heard)
	}
}

func Test_current_returns_a_copy(t *testing.T) {
	s := Open(t.TempDir(), compiled, quiet)
	s.Current()[0] = "mutated"
	if s.Current()[0] != compiled {
		t.Fatal("Current exposes the store's own slice")
	}
}

// A manifest dated far ahead -- a typo, a wrong clock on the signing
// machine -- would otherwise lock the list against every later release.
func Test_a_statement_dated_in_the_future_is_refused(t *testing.T) {
	s := Open(t.TempDir(), compiled, quiet)
	s.now = func() time.Time { return at(10) }
	if changed, err := s.Offer([]string{"http://203.0.113.7/vpn"}, at(10).Add(25*time.Hour)); err == nil || changed {
		t.Fatalf("Offer from tomorrow and a bit = %v, %v; want refused", changed, err)
	}
	if changed, err := s.Offer([]string{"http://203.0.113.7/vpn"}, at(10).Add(23*time.Hour)); err != nil || !changed {
		t.Fatalf("Offer within a day of skew = %v, %v; want taken", changed, err)
	}
}

// A manifest without the field still raises the bar: otherwise an older
// manifest naming an address we gave up is taken on a store that has
// never seen a list.
func Test_a_newer_statement_without_a_list_still_raises_the_bar(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, compiled, quiet)
	if changed, err := s.Offer(nil, at(10)); err != nil || changed {
		t.Fatalf("Offer(nil) = %v, %v", changed, err)
	}
	if got := s.Current(); !reflect.DeepEqual(got, []string{compiled}) {
		t.Fatalf("Current = %v, want the compiled address", got)
	}
	again := Open(dir, compiled, quiet)
	if changed, _ := again.Offer([]string{"http://192.0.2.66/vpn"}, at(9)); changed {
		t.Fatal("an older statement was taken after a newer one without a list")
	}
	if changed, _ := again.Offer([]string{"http://203.0.113.7/vpn"}, at(11)); !changed {
		t.Fatal("a newer list was not taken")
	}
}
