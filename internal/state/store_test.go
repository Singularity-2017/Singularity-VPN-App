package state

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func Test_round_trip_preserves_state(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	want := State{
		Client_id:        "c1",
		Device_id:        "d1",
		Last_node_id:     "n1",
		Last_protocol:    "wireguard",
		Connect_on_start: true,
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Client_id != want.Client_id || got.Device_id != want.Device_id ||
		got.Last_node_id != want.Last_node_id || got.Last_protocol != want.Last_protocol ||
		got.Connect_on_start != want.Connect_on_start {
		t.Fatalf("round trip did not preserve state: got %+v, want %+v", got, want)
	}
	if got.Version != 1 {
		t.Fatalf("Version = %d, want 1", got.Version)
	}
}

func Test_missing_file_returns_the_zero_state(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load on a missing file returned an error: %v", err)
	}
	if got != (State{}) {
		t.Fatalf("Load on a missing file returned %+v, want the zero value", got)
	}
}

func Test_corrupt_file_returns_err_corrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("not json at all"), 0o600); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := store.Load()
	if !errors.Is(err, Err_corrupt) {
		t.Fatalf("Load on a corrupt file returned %v, want Err_corrupt", err)
	}
	if got != (State{}) {
		t.Fatalf("Load on a corrupt file returned %+v, want the zero value", got)
	}

	// The store must not have touched the corrupt file: nothing is deleted.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("corrupt file was removed or renamed: %v", err)
	}
}

func Test_future_version_returns_err_future_version(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"Version":999,"Client_id":"c1"}`), 0o600); err != nil {
		t.Fatalf("writing future-version file: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := store.Load()
	if !errors.Is(err, Err_future_version) {
		t.Fatalf("Load on a future-version file returned %v, want Err_future_version", err)
	}
	if got != (State{}) {
		t.Fatalf("Load on a future-version file returned %+v, want the zero value", got)
	}
}

func Test_save_writes_atomically_leaving_no_stray_temp_file(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Save(State{Client_id: "c1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory contains %v, want exactly [state.json]", names)
	}
}

func Test_save_stamps_updated_at_from_the_injected_clock(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store, err := Open_with_clock(filepath.Join(dir, "state.json"), func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("Open_with_clock: %v", err)
	}

	if err := store.Save(State{Client_id: "c1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Updated_at.Equal(fixed) {
		t.Fatalf("Updated_at = %v, want %v", got.Updated_at, fixed)
	}
}

func Test_concurrent_save_and_load_do_not_interleave(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Save(State{Client_id: "seed"}); err != nil {
		t.Fatalf("seeding Save: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = store.Save(State{Client_id: "concurrent", Last_node_id: string(rune('a' + i%26))})
		}(i)
		go func() {
			defer wg.Done()
			if _, err := store.Load(); err != nil {
				t.Errorf("concurrent Load failed: %v", err)
			}
		}()
	}
	wg.Wait()

	if _, err := store.Load(); err != nil {
		t.Fatalf("final Load failed, state was left corrupt: %v", err)
	}
}
