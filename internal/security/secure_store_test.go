package security

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// xor_cipher is a trivial, real (non-identity) cipher defined for tests: it
// proves a File_store does not write plaintext to disk without needing a
// platform-specific cipher.
type xor_cipher struct{ key byte }

func (c xor_cipher) Encrypt(plaintext []byte) ([]byte, error) {
	out := make([]byte, len(plaintext))
	for i, b := range plaintext {
		out[i] = b ^ c.key
	}
	return out, nil
}

func (c xor_cipher) Decrypt(ciphertext []byte) ([]byte, error) {
	return c.Encrypt(ciphertext) // XOR is its own inverse
}

// failing_cipher always fails to decrypt, standing in for a real cipher
// meeting data it cannot make sense of (wrong machine, wrong user, DPAPI
// blob from a reinstalled OS).
type failing_cipher struct{}

func (failing_cipher) Encrypt(plaintext []byte) ([]byte, error) { return plaintext, nil }
func (failing_cipher) Decrypt(ciphertext []byte) ([]byte, error) {
	return nil, fmt.Errorf("failing_cipher: refuses to decrypt")
}

func Test_memory_store_set_get_delete_round_trip(t *testing.T) {
	var store Memory_store

	if err := store.Set(Key_device_id, []byte("dev-1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(Key_device_id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "dev-1" {
		t.Fatalf("Get returned %q, want %q", got, "dev-1")
	}

	if err := store.Delete(Key_device_id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(Key_device_id); !errors.Is(err, Err_not_found) {
		t.Fatalf("Get after Delete returned %v, want Err_not_found", err)
	}
}

func Test_memory_store_get_on_absent_key_returns_err_not_found(t *testing.T) {
	var store Memory_store
	if _, err := store.Get(Key_device_token); !errors.Is(err, Err_not_found) {
		t.Fatalf("Get on an absent key returned %v, want Err_not_found", err)
	}
}

func Test_file_store_set_get_delete_round_trip(t *testing.T) {
	dir := t.TempDir()
	store, err := Open_file_store(filepath.Join(dir, "secrets.json"), No_cipher)
	if err != nil {
		t.Fatalf("Open_file_store: %v", err)
	}

	if err := store.Set(Key_device_token, []byte("svpn_abc")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(Key_device_token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "svpn_abc" {
		t.Fatalf("Get returned %q, want %q", got, "svpn_abc")
	}

	if err := store.Delete(Key_device_token); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(Key_device_token); !errors.Is(err, Err_not_found) {
		t.Fatalf("Get after Delete returned %v, want Err_not_found", err)
	}
}

func Test_file_store_get_on_absent_key_returns_err_not_found(t *testing.T) {
	dir := t.TempDir()
	store, err := Open_file_store(filepath.Join(dir, "secrets.json"), No_cipher)
	if err != nil {
		t.Fatalf("Open_file_store: %v", err)
	}
	if _, err := store.Get(Key_device_id); !errors.Is(err, Err_not_found) {
		t.Fatalf("Get on an absent key returned %v, want Err_not_found", err)
	}
}

func Test_file_store_survives_being_reopened(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")

	first, err := Open_file_store(path, xor_cipher{key: 0x5a})
	if err != nil {
		t.Fatalf("Open_file_store: %v", err)
	}
	if err := first.Set(Key_device_id, []byte("dev-1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	second, err := Open_file_store(path, xor_cipher{key: 0x5a})
	if err != nil {
		t.Fatalf("reopening Open_file_store: %v", err)
	}
	got, err := second.Get(Key_device_id)
	if err != nil {
		t.Fatalf("Get after reopening: %v", err)
	}
	if string(got) != "dev-1" {
		t.Fatalf("Get after reopening returned %q, want %q", got, "dev-1")
	}
}

func Test_file_store_decrypt_failure_is_reported_not_swallowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")

	writer, err := Open_file_store(path, No_cipher)
	if err != nil {
		t.Fatalf("Open_file_store: %v", err)
	}
	if err := writer.Set(Key_device_token, []byte("svpn_abc")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	reader, err := Open_file_store(path, failing_cipher{})
	if err != nil {
		t.Fatalf("Open_file_store: %v", err)
	}
	value, err := reader.Get(Key_device_token)
	if err == nil {
		t.Fatalf("Get with a failing cipher returned no error, value %q", value)
	}
	if len(value) != 0 {
		t.Fatalf("Get with a failing cipher returned a non-empty value %q alongside an error", value)
	}
}

func Test_file_store_does_not_write_plaintext_to_disk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")

	store, err := Open_file_store(path, xor_cipher{key: 0x42})
	if err != nil {
		t.Fatalf("Open_file_store: %v", err)
	}
	secret := []byte("svpn_super_secret_device_token")
	if err := store.Set(Key_device_token, secret); err != nil {
		t.Fatalf("Set: %v", err)
	}

	on_disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if bytes.Contains(on_disk, secret) {
		t.Fatalf("file on disk contains the plaintext secret")
	}
}

func Test_no_cipher_is_the_identity(t *testing.T) {
	plaintext := []byte("hello")
	ciphertext, err := No_cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if string(ciphertext) != string(plaintext) {
		t.Fatalf("No_cipher.Encrypt changed the input: %q", ciphertext)
	}
	decoded, err := No_cipher.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(decoded) != string(plaintext) {
		t.Fatalf("No_cipher.Decrypt changed the input: %q", decoded)
	}
}
