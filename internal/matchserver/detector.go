package matchserver

import (
	"errors"
	"net/netip"
	"sync"
	"time"
)

// Detector follows matches as they are measured: it counts each one, keeps
// a detection when it ends, and holds the server it named for the player
// to confirm, during the match and after it, until they answer or another
// match of the same game begins.
type Detector struct {
	mu    sync.Mutex
	store *Store
	open  *open_match
	// offered is, per game, the match whose server waits for an answer:
	// the open one, or the last that ended.
	offered map[string]*open_match
}

// New_detector builds a detector that records into store.
func New_detector(store *Store) *Detector {
	return &Detector{store: store, offered: map[string]*open_match{}}
}

// Stream counts one measurement that found the games streaming to one
// server: the packets sent each destination since the last. The first
// after an End begins a new match. game is the game playing, empty when
// that could not be told; region is the one boosted toward.
func (d *Detector) Stream(at time.Time, game, region string, sent map[netip.AddrPort]int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.open == nil {
		d.open = &open_match{sent: map[netip.AddrPort]int{}}
	}
	d.open.add(at, game, region, sent)
	if g := d.open.game; g != "" && !d.open.answered {
		d.offered[g] = d.open
	}
}

// End closes the match being counted and records its detection. It
// reports the detection, and false when no match was open.
func (d *Detector) End() (Candidate, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.open == nil {
		return Candidate{}, false, nil
	}
	c := d.open.candidate()
	d.open = nil
	return c, true, d.store.detected(c)
}

// Offer is the server named for game's current or last match, while the
// player has not answered about it; false when there is none, or the
// counts named none.
func (d *Detector) Offer(game string) (Candidate, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.offered[game]
	if !ok {
		return Candidate{}, false
	}
	c := m.candidate()
	return c, c.Named()
}

// Error_offer_changed is the answer to a server that is no longer the one
// offered: the counts moved, or the offer was answered meanwhile.
var Error_offer_changed = errors.New("matchserver: the server offered has changed")

// Answer records the player's answer about server, the one they were
// offered for game, and withdraws the offer.
func (d *Detector) Answer(game string, server netip.AddrPort, answer Answer, shown netip.Addr, at time.Time) (Confirmation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.offered[game]
	if !ok {
		return Confirmation{}, Error_offer_changed
	}
	c := m.candidate()
	if !c.Named() || c.Server != server {
		return Confirmation{}, Error_offer_changed
	}
	confirmation, err := Confirm(c, answer, shown, at)
	if err != nil {
		return Confirmation{}, err
	}
	delete(d.offered, game)
	m.answered = true
	return confirmation, d.store.confirmed(confirmation)
}

// Tally is the tally for game.
func (d *Detector) Tally(game string) Tally {
	return d.store.Tally(game)
}
