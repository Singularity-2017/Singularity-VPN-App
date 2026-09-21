package wireguard

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Key is a WireGuard Curve25519 key, private or public, always exactly 32
// bytes. It has no MarshalText or MarshalJSON on purpose: a key must not
// become serialisable by accident.
type Key [32]byte

// Parse_key decodes a standard-base64-encoded WireGuard key, the form
// wg-quick configs use. A decoded value that is not exactly 32 bytes is
// refused, naming the length it got: a key of any other length cannot be a
// Curve25519 key, and silently truncating or padding one would hand
// wireguard-go a key that is not the one the config named.
func Parse_key(text string) (Key, error) {
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return Key{}, fmt.Errorf("wireguard: key is not valid base64: %w", err)
	}
	if len(decoded) != 32 {
		return Key{}, fmt.Errorf("wireguard: key is %d bytes, want 32", len(decoded))
	}
	var key Key
	copy(key[:], decoded)
	return key, nil
}

// Hex renders the key as lower-case hex, 64 characters -- the form
// wireguard-go's UAPI wants.
func (k Key) Hex() string {
	return hex.EncodeToString(k[:])
}

// Is_zero reports whether the key is the zero value. For a Peer's
// Preshared_key, zero means the config carried none -- see Peer.Has_preshared,
// which is the field that actually distinguishes the two, since an all-zero
// key is technically decodable too.
func (k Key) Is_zero() bool {
	return k == Key{}
}

// String never prints the key material. This mirrors
// protocols.Connection_config.String() and exists for the same reason: one
// careless %v on a Key in a log line later must not be able to leak a
// tunnel key.
func (k Key) String() string {
	return "wireguard.Key(not shown)"
}
