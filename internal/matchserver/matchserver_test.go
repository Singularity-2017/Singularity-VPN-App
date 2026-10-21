package matchserver

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	server_a = netip.MustParseAddrPort("203.0.113.10:7086")
	server_b = netip.MustParseAddrPort("203.0.113.20:7086")
	server_c = netip.MustParseAddrPort("198.51.100.5:443")
	at       = time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
)

func Test_busiest_names_an_address_that_got_most_of_the_packets(t *testing.T) {
	a_other_port := netip.AddrPortFrom(server_a.Addr(), 7087)
	server, most, total, ok := Busiest(map[netip.AddrPort]int{server_a: 40, a_other_port: 30, server_b: 20}, 50)
	if !ok || server != server_a.Addr() || most != 70 || total != 90 {
		t.Fatalf("Busiest = %v %d %d %v, want %v 70 90 true", server, most, total, ok, server_a.Addr())
	}
	if _, _, _, ok := Busiest(map[netip.AddrPort]int{server_a: 40}, 50); ok {
		t.Fatal("fewer packets than the least named a server")
	}
	if _, _, _, ok := Busiest(map[netip.AddrPort]int{server_a: 60, server_b: 60}, 50); ok {
		t.Fatal("a tie named a server")
	}
	if _, _, _, ok := Busiest(nil, 0); ok {
		t.Fatal("nothing sent named a server")
	}
}

func Test_a_match_is_counted_whole_and_its_runner_up_kept(t *testing.T) {
	d := New_detector(must_store(t, ""))
	d.Stream(at, "pubg", "ru", map[netip.AddrPort]int{server_a: 100, server_b: 90, server_c: 5})
	d.Stream(at.Add(5*time.Second), "pubg", "ru", map[netip.AddrPort]int{server_a: 100, server_b: 95})
	c, ok, err := d.End()
	if err != nil || !ok {
		t.Fatalf("End = %v %v", ok, err)
	}
	if c.Server != server_a || c.Packets != 200 || c.Runner_up != 185 || c.Total != 390 {
		t.Fatalf("candidate %+v, want %v 200 behind 185 of 390", c, server_a)
	}
	if c.Game != "pubg" || c.Region != "ru" || !c.First.Equal(at) || !c.Last.Equal(at.Add(5*time.Second)) {
		t.Fatalf("candidate %+v: game, region or times wrong", c)
	}
	if len(c.Top) != 3 || c.Top[0].Address != server_a || c.Top[1].Address != server_b || c.Top[2].Address != server_c {
		t.Fatalf("top %+v", c.Top)
	}
	if _, ok, _ := d.End(); ok {
		t.Fatal("a second End found a match")
	}
}

func Test_a_tie_names_no_server_but_keeps_the_counts(t *testing.T) {
	d := New_detector(must_store(t, ""))
	d.Stream(at, "pubg", "", map[netip.AddrPort]int{server_a: 80, server_b: 80})
	if _, ok := d.Offer("pubg"); ok {
		t.Fatal("a tie was offered")
	}
	c, _, _ := d.End()
	if c.Named() || c.Packets != 80 || c.Runner_up != 80 || len(c.Top) != 2 {
		t.Fatalf("candidate %+v, want no server and both counts", c)
	}
}

func Test_answers_land_in_the_tally_and_a_dismissal_in_neither_number(t *testing.T) {
	dir := t.TempDir()
	d := New_detector(must_store(t, dir))
	answer := func(a Answer, shown string) Confirmation {
		t.Helper()
		d.Stream(at, "pubg", "ru", map[netip.AddrPort]int{server_a: 100})
		address, err := Parse_shown(shown)
		if err != nil {
			t.Fatal(err)
		}
		c, err := d.Answer("pubg", server_a, a, address, at)
		if err != nil {
			t.Fatal(err)
		}
		d.End()
		return c
	}
	if c := answer(Answer_yes, ""); c.Verdict != Verdict_right || c.Shown != server_a.Addr() {
		t.Fatalf("yes: %+v", c)
	}
	if c := answer(Answer_no, "203.0.113.99:7086"); c.Verdict != Verdict_wrong || c.Shown != netip.MustParseAddr("203.0.113.99") {
		t.Fatalf("correction: %+v", c)
	}
	if c := answer(Answer_no, "203.0.113.10"); c.Verdict != Verdict_right {
		t.Fatalf("a no naming the same address: %+v", c)
	}
	if c := answer(Answer_dismiss, ""); c.Verdict != Verdict_dismissed {
		t.Fatalf("dismissal: %+v", c)
	}
	if got := d.Tally("pubg"); got != (Tally{Right: 2, Confirmed: 3}) {
		t.Fatalf("tally %+v, want 2 of 3", got)
	}
	reopened := must_store(t, dir)
	if got := reopened.Tally("pubg"); got != (Tally{Right: 2, Confirmed: 3}) {
		t.Fatalf("tally read back %+v, want 2 of 3", got)
	}
	detections, _ := os.ReadFile(filepath.Join(dir, detections_file))
	if n := strings.Count(string(detections), "\n"); n != 4 {
		t.Fatalf("%d detections recorded, want 4", n)
	}
}

func Test_an_answered_match_is_not_offered_again_and_a_stale_answer_is_refused(t *testing.T) {
	d := New_detector(must_store(t, ""))
	d.Stream(at, "pubg", "", map[netip.AddrPort]int{server_a: 100})
	if _, err := d.Answer("pubg", server_b, Answer_yes, netip.Addr{}, at); !errors.Is(err, Error_offer_changed) {
		t.Fatalf("an answer about another server: %v", err)
	}
	if _, err := d.Answer("pubg", server_a, Answer_dismiss, netip.Addr{}, at); err != nil {
		t.Fatal(err)
	}
	d.Stream(at.Add(5*time.Second), "pubg", "", map[netip.AddrPort]int{server_a: 100})
	if _, ok := d.Offer("pubg"); ok {
		t.Fatal("the answered match was offered again")
	}
	d.End()
	d.Stream(at.Add(time.Minute), "pubg", "", map[netip.AddrPort]int{server_b: 100})
	if c, ok := d.Offer("pubg"); !ok || c.Server != server_b {
		t.Fatalf("the next match offered %+v %v", c, ok)
	}
	if got := d.Tally("pubg"); got.Confirmed != 0 {
		t.Fatalf("a dismissal counted: %+v", got)
	}
}

func must_store(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open_store(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
