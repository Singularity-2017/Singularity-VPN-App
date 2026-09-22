//go:build windows

package security

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Dpapi_cipher implements Cipher with DPAPI at machine scope.
//
// What it protects against, and what it does not -- a reader who assumes
// DPAPI is doing more than it is will put something in this store that
// needs more:
//
//   - The scope is the machine, not a user, because the service runs as
//     LocalSystem and must read the device token before anybody has logged
//     in. That is what lets "connect on start" mean the machine rather than
//     a session (design section 2).
//   - Machine scope means any code on this machine able to read the file
//     can also decrypt it. It is not a secret from an administrator and was
//     never meant to be. What it does buy is that the file is useless once
//     copied off the machine -- a backup, a stolen disk, a sync folder --
//     and that is a real class of loss.
//   - The protection that matters on the machine itself is therefore the
//     ACL on the directory the file lives in, which cmd/svpn-service sets.
//     If that directory is ever made readable to everyone, this cipher will
//     not save anything.
type Dpapi_cipher struct{}

func New_dpapi_cipher() Dpapi_cipher { return Dpapi_cipher{} }

// dpapi_entropy is passed to both calls as optionalEntropy. It is compiled
// into the binary, so it is not a secret and adds no strength against
// anybody holding the file. It is domain separation: a blob this
// application produced cannot be decrypted by another program on the same
// machine that calls DPAPI without entropy, nor theirs by this one. It is
// not a key and must not be described as one.
var dpapi_entropy = []byte("singularity-vpn-app/security/v1")

const dpapi_flags = windows.CRYPTPROTECT_LOCAL_MACHINE | windows.CRYPTPROTECT_UI_FORBIDDEN

// Encrypt refuses an empty plaintext by name. DPAPI refuses it too, with
// "The parameter is incorrect", which would send somebody looking for a
// bad argument in the wrong place; and an empty credential is not one
// worth storing.
func (Dpapi_cipher) Encrypt(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("security: dpapi encrypt: refusing to store an empty value")
	}
	var out windows.DataBlob
	err := windows.CryptProtectData(blob_of(plaintext), nil, blob_of(dpapi_entropy), 0, nil, dpapi_flags, &out)
	if err != nil {
		return nil, fmt.Errorf("security: dpapi encrypt: %w", err)
	}
	return take_blob(&out), nil
}

func (Dpapi_cipher) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, errors.New("security: dpapi decrypt: empty ciphertext")
	}
	var out windows.DataBlob
	err := windows.CryptUnprotectData(blob_of(ciphertext), nil, blob_of(dpapi_entropy), 0, nil, dpapi_flags, &out)
	if err != nil {
		return nil, fmt.Errorf("security: dpapi decrypt: %w", err)
	}
	return take_blob(&out), nil
}

// blob_of points a DataBlob at data without copying it. An empty slice is
// a zero blob: taking &data[0] of it would panic.
func blob_of(data []byte) *windows.DataBlob {
	if len(data) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

// take_blob copies a DPAPI output blob into Go memory and frees it. Every
// successful call goes through here, so the LocalFree cannot be forgotten
// on one path: a service that leaked a blob per sign-in would live for
// months doing it. On failure DPAPI allocates nothing, so there is nothing
// to free there.
func take_blob(blob *windows.DataBlob) []byte {
	if blob.Data == nil {
		return []byte{}
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(blob.Data)))
	out := make([]byte, blob.Size)
	copy(out, unsafe.Slice(blob.Data, blob.Size))
	return out
}
