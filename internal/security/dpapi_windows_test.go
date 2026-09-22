//go:build windows

package security

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DPAPI at machine scope encrypts for any caller, so these need no
// privilege and are real: nothing here is faked.

func Test_dpapi_round_trips_and_hides_the_plaintext(t *testing.T) {
	cipher := New_dpapi_cipher()
	plaintext := []byte("device-token-that-must-not-appear-in-the-file")

	ciphertext, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatal("the ciphertext contains the plaintext")
	}

	got, err := cipher.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt(Encrypt(x)) = %q, want %q", got, plaintext)
	}
}

func Test_dpapi_refuses_corrupted_bytes(t *testing.T) {
	cipher := New_dpapi_cipher()
	ciphertext, err := cipher.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ciphertext[len(ciphertext)-1] ^= 0xff

	if got, err := cipher.Decrypt(ciphertext); err == nil {
		t.Fatalf("Decrypt(corrupted) = %q, nil; want an error rather than garbage", got)
	}
}

func Test_dpapi_refuses_an_empty_value_by_name(t *testing.T) {
	for _, empty := range [][]byte{nil, {}} {
		_, err := New_dpapi_cipher().Encrypt(empty)
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("Encrypt(%v) = %v, want a refusal that says the value is empty", empty, err)
		}
	}
}

// Test_dpapi_entropy_separates_this_application pins the domain separation
// the entropy exists for: a blob made without it -- by any other program on
// the machine -- does not decrypt here.
func Test_dpapi_entropy_separates_this_application(t *testing.T) {
	saved := dpapi_entropy
	dpapi_entropy = nil
	foreign, err := New_dpapi_cipher().Encrypt([]byte("secret"))
	dpapi_entropy = saved
	if err != nil {
		t.Fatalf("Encrypt without entropy: %v", err)
	}

	if _, err := New_dpapi_cipher().Decrypt(foreign); err == nil {
		t.Fatal("a blob made without this application's entropy decrypted here")
	}
}

// Test_a_file_store_over_dpapi_keeps_no_plaintext_on_disk is the store the
// service actually opens, end to end.
func Test_a_file_store_over_dpapi_keeps_no_plaintext_on_disk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	store, err := Open_file_store(path, New_dpapi_cipher())
	if err != nil {
		t.Fatalf("Open_file_store: %v", err)
	}
	if err := store.Set(Key_device_token, []byte("tok-123456")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(Key_device_token)
	if err != nil || string(got) != "tok-123456" {
		t.Fatalf("Get = %q, %v; want the stored token", got, err)
	}
	// The file holds base64, so a check for the literal alone would pass
	// with no cipher at all; the encoded form is the one that would leak.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the store: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("tok-123456"))
	if bytes.Contains(raw, []byte("tok-123456")) || bytes.Contains(raw, []byte(encoded)) {
		t.Fatal("the device token is readable in the file")
	}
}
