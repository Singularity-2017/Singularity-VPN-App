package security

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Secure_store is the credential abstraction fixed by docs/CLIENT_CORE.md
// §28. Core depends on this interface and never on which mechanism sits
// behind it.
type Secure_store interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// Err_not_found is what Get returns for a key that was never Set, or was
// Deleted -- never an empty slice, so a caller cannot mistake "no secret"
// for "an empty secret".
var Err_not_found = errors.New("security: no such key")

// Key constants for the credentials this application stores. A password is
// never a key here: the interface passes a password through once at
// sign-in, and the application never stores it.
const (
	Key_device_token = "device_token"
	Key_device_id    = "device_id"
)

// Memory_store is a Secure_store backed by a map behind a mutex. It is what
// every other package's tests use, which is why it lives here rather than
// in a _test.go file. Its zero value is ready to use.
type Memory_store struct {
	mutex  sync.Mutex
	values map[string][]byte
}

func (m *Memory_store) Get(key string) ([]byte, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	value, ok := m.values[key]
	if !ok {
		return nil, Err_not_found
	}
	out := make([]byte, len(value))
	copy(out, value)
	return out, nil
}

func (m *Memory_store) Set(key string, value []byte) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.values == nil {
		m.values = make(map[string][]byte)
	}
	stored := make([]byte, len(value))
	copy(stored, value)
	m.values[key] = stored
	return nil
}

func (m *Memory_store) Delete(key string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	delete(m.values, key)
	return nil
}

// Cipher is what protects a File_store's values at rest. DPAPI at machine
// scope is the real Cipher this application ships, and it is not written
// here: it is Windows-only, it cannot be unit tested on this machine, and it
// belongs behind platform/windows in stage 3.
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// no_cipher is the identity Cipher.
type no_cipher struct{}

func (no_cipher) Encrypt(plaintext []byte) ([]byte, error) { return plaintext, nil }

func (no_cipher) Decrypt(ciphertext []byte) ([]byte, error) { return ciphertext, nil }

// No_cipher does not protect anything -- it exists for tests and for tests
// only, standing in until platform/windows supplies the real DPAPI cipher in
// stage 3.
var No_cipher Cipher = no_cipher{}

// File_store is a Secure_store backed by a JSON file of base64-encoded
// values, each passed through an injected Cipher before it is written.
type File_store struct {
	path   string
	cipher Cipher
	mutex  sync.Mutex
}

// Open_file_store opens the store at path, creating its directory. It does
// not read the file -- a missing file behaves as an empty store.
func Open_file_store(path string, cipher Cipher) (*File_store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("security: creating %s: %w", dir, err)
	}
	return &File_store{path: path, cipher: cipher}, nil
}

func (f *File_store) Get(key string) ([]byte, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	values, err := f.read()
	if err != nil {
		return nil, err
	}
	encoded, ok := values[key]
	if !ok {
		return nil, Err_not_found
	}
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("security: decoding stored value for %q: %w", key, err)
	}
	plaintext, err := f.cipher.Decrypt(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("security: decrypting value for %q: %w", key, err)
	}
	return plaintext, nil
}

func (f *File_store) Set(key string, value []byte) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	values, err := f.read()
	if err != nil {
		return err
	}
	ciphertext, err := f.cipher.Encrypt(value)
	if err != nil {
		return fmt.Errorf("security: encrypting value for %q: %w", key, err)
	}
	values[key] = base64.StdEncoding.EncodeToString(ciphertext)
	return f.write(values)
}

func (f *File_store) Delete(key string) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	values, err := f.read()
	if err != nil {
		return err
	}
	delete(values, key)
	return f.write(values)
}

// read loads the file's key -> base64(ciphertext) map. A missing file reads
// as an empty store, the same rule state.Store.Load uses for its file.
func (f *File_store) read() (map[string]string, error) {
	raw, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("security: reading %s: %w", f.path, err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("security: file is corrupt: %w", err)
	}
	return values, nil
}

func (f *File_store) write(values map[string]string) error {
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return fmt.Errorf("security: encoding store: %w", err)
	}
	return atomic_write(f.path, data)
}

// atomic_write is the write-temp-sync-rename sequence that keeps a crash
// from ever leaving half a file behind, the same approach state.Store uses
// for its own file.
func atomic_write(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".security-*.tmp")
	if err != nil {
		return fmt.Errorf("security: creating temporary file: %w", err)
	}
	tmp_path := tmp.Name()
	defer os.Remove(tmp_path) // no-op once the rename below has succeeded

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("security: writing temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("security: syncing temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("security: closing temporary file: %w", err)
	}
	if err := os.Rename(tmp_path, path); err != nil {
		return fmt.Errorf("security: renaming temporary file into place: %w", err)
	}
	return nil
}
