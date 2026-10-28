package update

import (
	"context"
	"crypto/ed25519"
	"reflect"
	"testing"
	"time"
)

// fixed is a control plane source that always names base; empty names none.
func fixed(base string) func() []string {
	return func() []string {
		if base == "" {
			return nil
		}
		return []string{base}
	}
}

type offer struct {
	list        []string
	released_at time.Time
}

func listen_for_offers(h *harness) *[]offer {
	var heard []offer
	h.updater.config.Offer_control_planes = func(list []string, released_at time.Time) {
		heard = append(heard, offer{list, released_at})
	}
	return &heard
}

func Test_a_verified_manifest_offers_its_control_planes(t *testing.T) {
	public, private := new_key(t)
	moved := []string{"http://203.0.113.7/vpn"}
	released := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	r := make_release(t, private, "v1.0.0", func(m *Manifest) {
		m.Control_planes = moved
		m.Released_at = released
	})
	mirror := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, "", mirror.URL)
	heard := listen_for_offers(h)

	if _, err := h.updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*heard) != 1 || !reflect.DeepEqual((*heard)[0].list, moved) || !(*heard)[0].released_at.Equal(released) {
		t.Fatalf("offers = %+v, want one with %v at %v", *heard, moved, released)
	}
}

// The whole point: an address nobody signed never reaches the store.
func Test_an_unverified_manifest_offers_nothing(t *testing.T) {
	public, _ := new_key(t)
	_, stranger := new_key(t)
	r := make_release(t, stranger, "v1.1.0", func(m *Manifest) {
		m.Control_planes = []string{"http://192.0.2.66/vpn"}
	})
	mirror := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, "", mirror.URL)
	heard := listen_for_offers(h)

	h.updater.Check(context.Background())
	if len(*heard) != 0 {
		t.Fatalf("offers = %+v, want none", *heard)
	}
}

func Test_a_manifest_older_than_this_build_offers_nothing(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v0.9.0", func(m *Manifest) {
		m.Control_planes = []string{"http://192.0.2.66/vpn"}
	})
	mirror := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, "", mirror.URL)
	heard := listen_for_offers(h)

	h.updater.Check(context.Background())
	if len(*heard) != 0 {
		t.Fatalf("offers = %+v, want none", *heard)
	}
}

// After a move the updater asks the control plane at its current address,
// not the compiled one; every current address is a channel.
func Test_every_current_control_plane_address_is_a_channel(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	gone := serve(t, nil, nil)
	moved := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, "", "")
	h.updater.config.Control_planes = func() []string { return []string{gone.URL, moved.URL} }

	staged, err := h.updater.Check(context.Background())
	if err != nil || !staged {
		t.Fatalf("Check = %v, %v; want staged from the second address", staged, err)
	}
}

// A control plane that stops answering may have moved; the next check then
// comes early, so the signed list of where it went is fetched from the
// mirrors within minutes instead of hours.
func Test_a_long_silence_brings_the_check_forward_once_an_hour(t *testing.T) {
	public, _ := new_key(t)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, "", "")
	u := h.updater
	start := h.now

	u.Control_plane_answered(false)
	h.now = start.Add(29 * time.Minute)
	u.Control_plane_answered(false)
	if u.early.Load() {
		t.Fatal("early check after 29 minutes of silence, want 30")
	}
	h.now = start.Add(30 * time.Minute)
	u.Control_plane_answered(false)
	if !u.early.Swap(false) {
		t.Fatal("no early check after 30 minutes of silence")
	}
	h.now = start.Add(89 * time.Minute)
	u.Control_plane_answered(false)
	if u.early.Load() {
		t.Fatal("a second early check within the hour")
	}
	h.now = start.Add(90 * time.Minute)
	u.Control_plane_answered(false)
	if !u.early.Load() {
		t.Fatal("no early check an hour after the first")
	}
}

func Test_an_answer_ends_the_silence(t *testing.T) {
	public, _ := new_key(t)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, "", "")
	u := h.updater
	start := h.now

	u.Control_plane_answered(false)
	h.now = start.Add(20 * time.Minute)
	u.Control_plane_answered(true)
	h.now = start.Add(40 * time.Minute)
	u.Control_plane_answered(false)
	if u.early.Load() {
		t.Fatal("silence counted across an answer")
	}
}
