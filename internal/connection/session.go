package connection

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Session is one established tunnel: a switch ends the old one and starts a
// new one, because a session identifies a tunnel and a different node is a
// different tunnel. Id is what correlates a report made here with a log
// line on the control plane, so it must never be reused across two
// different tunnels.
type Session struct {
	Id         string
	Node_id    string
	Protocol   string
	Started_at time.Time
}

// session_id_generation_failed is what default_session_id returns when
// crypto/rand itself failed. It is deliberately not empty and not a
// plausible-looking id: an empty string could be mistaken for "no session
// yet" by something that forgot to check the ok return, and a fixed-shape
// random-looking value would silently correlate two different tunnels under
// one indistinguishable id. This string cannot be a real id -- session_ok
// treats it, and only it besides empty, as unusable -- so a caller that
// checks is told the generator is broken the moment it looks, in a log
// line, in a Session, in a test, rather than being handed a false identity
// to build on. New_manager's default wiring lets a Connect built on it fail
// rather than proceed.
const session_id_generation_failed = "id-generation-failed"

// default_session_id is the New_session_id a Manager gets when Dependencies
// does not supply one: 16 bytes of crypto/rand, hex-encoded. It is used
// nowhere in this package except as that default -- everything else calls
// through the injected function, so a test can hand back a fixed sequence
// instead.
func default_session_id() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return session_id_generation_failed
	}
	return hex.EncodeToString(buffer)
}

// session_id_usable reports whether id is fit to identify a session: not
// empty, and not the sentinel default_session_id returns when the entropy
// source itself is broken.
func session_id_usable(id string) bool {
	return id != "" && id != session_id_generation_failed
}
