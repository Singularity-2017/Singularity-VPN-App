package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type release struct {
	manifest  []byte
	signature []byte
	setup     []byte
}

func new_key(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

func make_release(t *testing.T, private ed25519.PrivateKey, version string, edit func(*Manifest)) release {
	t.Helper()
	setup := []byte("setup for " + version)
	sum := sha256.Sum256(setup)
	m := Manifest{
		Schema:      Schema,
		Version:     version,
		Released_at: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
		Setup:       Artifact{File: "SingularityVPN-setup-" + strings.TrimPrefix(version, "v") + ".exe", Sha256: hex.EncodeToString(sum[:]), Size: int64(len(setup))},
	}
	if edit != nil {
		edit(&m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return release{manifest: raw, signature: []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw))), setup: setup}
}

// serve answers like the control plane's release endpoints.
func serve(t *testing.T, r *release, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r == nil {
			http.Error(w, "unavailable", http.StatusNotFound)
			return
		}
		switch req.URL.Path {
		case "/api/v1/app/update", "/manifest.json":
			w.Write(r.manifest)
		case "/api/v1/app/update.sig", "/manifest.json.sig":
			w.Write(r.signature)
		default:
			w.Write(r.setup)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

type harness struct {
	updater *Updater
	dir     string
	idle    bool
	now     time.Time
	applied []Staged
}

func new_harness(t *testing.T, current string, keys []ed25519.PublicKey, control_plane, fallback string) *harness {
	t.Helper()
	h := &harness{dir: t.TempDir(), idle: true, now: time.Now()}
	u, err := New(Config{
		Now:             func() time.Time { return h.now },
		Control_planes:  fixed(control_plane),
		Fallback:        fallback,
		Current_version: current,
		Keys:            keys,
		Dir:             h.dir,
		Idle:            func() bool { return h.idle },
		Apply:           func(s Staged) error { h.applied = append(h.applied, s); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.updater = u
	return h
}

func load(t *testing.T, dir string) State {
	t.Helper()
	s, err := Load_state(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func Test_a_newer_signed_release_is_staged_and_applied_when_idle(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")

	staged, err := h.updater.Check(context.Background())
	if err != nil || !staged {
		t.Fatalf("Check = %v, %v; want staged", staged, err)
	}
	launched, err := h.updater.Maybe_apply()
	if err != nil || !launched || len(h.applied) != 1 || h.applied[0].Version != "v1.1.0" {
		t.Fatalf("Maybe_apply = %v, %v, applied %+v; want v1.1.0 launched", launched, err, h.applied)
	}
	data, err := os.ReadFile(h.applied[0].Path)
	if err != nil || string(data) != string(r.setup) {
		t.Fatalf("staged file = %q, %v; want the release's setup", data, err)
	}
}

func Test_a_manifest_signed_by_an_unknown_key_is_refused(t *testing.T) {
	trusted, _ := new_key(t)
	_, stranger := new_key(t)
	r := make_release(t, stranger, "v9.0.0", nil)
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{trusted}, server.URL, "")

	staged, err := h.updater.Check(context.Background())
	if staged || !errors.Is(err, Err_bad_signature) {
		t.Fatalf("Check with a stranger's signature = %v, %v; want refused as a bad signature", staged, err)
	}
}

func Test_a_manifest_altered_after_signing_is_refused(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	r.manifest = []byte(strings.Replace(string(r.manifest), "v1.1.0", "v1.2.0", 1))
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")

	if staged, err := h.updater.Check(context.Background()); staged || !errors.Is(err, Err_bad_signature) {
		t.Fatalf("Check of an altered manifest = %v, %v; want refused", staged, err)
	}
}

func Test_an_artifact_that_does_not_match_its_hash_is_not_staged(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	r.setup = []byte("setup for v1.1.X") // same length, different bytes
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")

	if staged, err := h.updater.Check(context.Background()); staged || err == nil {
		t.Fatalf("Check with a tampered artifact = %v, %v; want an error and nothing staged", staged, err)
	}
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".exe") || strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("a tampered download left %s behind", e.Name())
		}
	}
}

func Test_the_same_or_an_older_release_is_not_staged(t *testing.T) {
	public, private := new_key(t)
	// The same version is the ordinary up-to-date answer. An older one is
	// below the replay floor, which starts at the running build, and is
	// reported as a channel failure.
	for version, want_error := range map[string]bool{"v1.0.0": false, "v0.9.9": true} {
		r := make_release(t, private, version, nil)
		server := serve(t, &r, nil)
		h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
		if staged, err := h.updater.Check(context.Background()); staged || (err != nil) != want_error {
			t.Fatalf("Check of %s from v1.0.0 = %v, %v; want nothing staged, error %v", version, staged, err, want_error)
		}
	}
}

func Test_a_non_urgent_update_waits_while_a_tunnel_is_up(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
	h.idle = false

	if _, err := h.updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if launched, _ := h.updater.Maybe_apply(); launched {
		t.Fatal("a non-urgent update was applied while a tunnel was carrying traffic")
	}
	h.idle = true
	if launched, _ := h.updater.Maybe_apply(); !launched {
		t.Fatal("the update was not applied once idle")
	}
}

func Test_an_urgent_update_or_one_below_minimum_does_not_wait(t *testing.T) {
	public, private := new_key(t)
	for name, edit := range map[string]func(*Manifest){
		"urgent":        func(m *Manifest) { m.Urgent = true },
		"below minimum": func(m *Manifest) { m.Minimum_version = "v1.0.5" },
	} {
		r := make_release(t, private, "v1.1.0", edit)
		server := serve(t, &r, nil)
		h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
		h.idle = false
		if _, err := h.updater.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if launched, _ := h.updater.Maybe_apply(); !launched {
			t.Fatalf("%s: not applied while a tunnel was up, want applied at once", name)
		}
	}
}

func Test_a_blocked_control_plane_falls_through_to_the_fallback_mirror(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	blocked := serve(t, nil, nil)
	mirror := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, blocked.URL, mirror.URL)

	if staged, err := h.updater.Check(context.Background()); err != nil || !staged {
		t.Fatalf("Check via the fallback = %v, %v; want staged", staged, err)
	}
}

func Test_the_mirror_list_comes_from_the_last_verified_manifest(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.0.0", func(m *Manifest) { m.Mirrors = []string{"https://mirror.example/releases"} })
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
	if _, err := h.updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := load(t, h.dir)
	if len(state.Mirrors) != 1 || state.Mirrors[0] != "https://mirror.example/releases" {
		t.Fatalf("mirrors after an up-to-date check = %v; want the signed list stored", state.Mirrors)
	}
}

func Test_a_version_that_failed_waits_a_day_before_it_is_tried_again(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
	if err := Record_failure(h.dir, "1.1.0", h.now); err != nil { // spelled without the v on purpose
		t.Fatal(err)
	}
	h.now = h.now.Add(23 * time.Hour)
	if staged, err := h.updater.Check(context.Background()); staged || err != nil {
		t.Fatalf("Check of a version that failed 23h ago = %v, %v; want it skipped", staged, err)
	}
	h.now = h.now.Add(2 * time.Hour)
	if staged, err := h.updater.Check(context.Background()); !staged || err != nil {
		t.Fatalf("Check of a version that failed 25h ago = %v, %v; want it tried again", staged, err)
	}
}

func Test_a_failed_version_below_minimum_is_retried_within_hours(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", func(m *Manifest) { m.Minimum_version = "v1.1.0" })
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
	Record_failure(h.dir, "v1.1.0", h.now)
	h.now = h.now.Add(2 * time.Hour)
	if staged, err := h.updater.Check(context.Background()); !staged || err != nil {
		t.Fatalf("Check below minimum 2h after a failure = %v, %v; want it tried again", staged, err)
	}
}

// A replayed old manifest is still validly signed. Whoever answers on the
// control plane's address must not be able to hold an installation back
// with it while a mirror serves the real latest release.
func Test_the_newest_verified_manifest_wins_over_a_replayed_old_one(t *testing.T) {
	public, private := new_key(t)
	old := make_release(t, private, "v1.1.0", func(m *Manifest) { m.Mirrors = []string{"https://127.0.0.1:1/attacker"} })
	latest := make_release(t, private, "v1.2.0", func(m *Manifest) { m.Mirrors = []string{"https://127.0.0.1:1/real"} })
	replaying := serve(t, &old, nil)
	mirror := serve(t, &latest, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, replaying.URL, mirror.URL)

	if staged, err := h.updater.Check(context.Background()); !staged || err != nil {
		t.Fatalf("Check = %v, %v; want staged", staged, err)
	}
	state := load(t, h.dir)
	if state.Staged == nil || state.Staged.Version != "v1.2.0" || state.Highest_version != "v1.2.0" {
		t.Fatalf("staged %+v, highest %q; want v1.2.0 for both", state.Staged, state.Highest_version)
	}

	// Later only the replaying channel answers: its manifest is refused,
	// and neither the staged release nor the signed mirror list moves back.
	mirror.Close()
	if staged, err := h.updater.Check(context.Background()); staged || err == nil {
		t.Fatalf("Check with only a replayed manifest = %v, %v; want refused", staged, err)
	}
	state = load(t, h.dir)
	if state.Staged == nil || state.Staged.Version != "v1.2.0" || len(state.Mirrors) != 1 || state.Mirrors[0] != "https://127.0.0.1:1/real" {
		t.Fatalf("after the replay: staged %+v, mirrors %v; want v1.2.0 and the real mirror kept", state.Staged, state.Mirrors)
	}
}

func Test_a_stalled_channel_does_not_hang_the_check(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	release_stall := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-release_stall
	}))
	t.Cleanup(stalled.Close)
	t.Cleanup(func() { close(release_stall) })
	mirror := serve(t, &r, nil)

	dir := t.TempDir()
	u, err := New(Config{
		Control_planes:  fixed(stalled.URL),
		Fallback:        mirror.URL,
		Current_version: "v1.0.0",
		Keys:            []ed25519.PublicKey{public},
		Dir:             dir,
		Idle:            func() bool { return true },
		Apply:           func(Staged) error { return nil },
		Request_timeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	staged, err := u.Check(context.Background())
	if !staged || err != nil || time.Since(began) > 5*time.Second {
		t.Fatalf("Check past a stalled channel = %v, %v after %v; want staged from the mirror promptly", staged, err, time.Since(began))
	}
}

func Test_a_corrupt_state_file_is_set_aside_rather_than_blocking_updates(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
	os.WriteFile(filepath.Join(h.dir, state_file), []byte("{\"staged\": {\"vers"), 0o600)

	if staged, err := h.updater.Check(context.Background()); !staged || err != nil {
		t.Fatalf("Check over a corrupt state = %v, %v; want staged", staged, err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, state_file+".corrupt")); err != nil {
		t.Fatalf("the corrupt state was not kept aside: %v", err)
	}
}

func Test_a_staged_release_is_not_replaced_by_an_older_one(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
	path := filepath.Join(h.dir, "SingularityVPN-setup-1.3.0.exe")
	os.WriteFile(path, []byte("newer"), 0o600)
	sum, _ := File_sha256(path)
	Save_state(h.dir, State{Staged: &Staged{Version: "v1.3.0", Path: path, Sha256: sum}})

	if _, err := h.updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := load(t, h.dir); state.Staged == nil || state.Staged.Version != "v1.3.0" {
		t.Fatalf("staged after an older manifest = %+v; want v1.3.0 kept", state.Staged)
	}
}

func Test_has_staged_reports_only_a_release_newer_than_this_build(t *testing.T) {
	h := new_harness(t, "v1.1.0", nil, "", "")
	if h.updater.Has_staged() {
		t.Fatal("Has_staged with nothing staged")
	}
	Save_state(h.dir, State{Staged: &Staged{Version: "v1.1.0"}})
	if h.updater.Has_staged() {
		t.Fatal("Has_staged for the version already running")
	}
	Save_state(h.dir, State{Staged: &Staged{Version: "v1.2.0"}})
	if !h.updater.Has_staged() {
		t.Fatal("Has_staged false for a newer staged release")
	}
}

func Test_a_staged_file_changed_after_verification_is_not_applied(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
	if _, err := h.updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := load(t, h.dir)
	os.WriteFile(state.Staged.Path, []byte("swapped after verification"), 0o600)
	if launched, err := h.updater.Maybe_apply(); launched || err == nil {
		t.Fatalf("Maybe_apply of a changed file = %v, %v; want refused", launched, err)
	}
	if len(h.applied) != 0 {
		t.Fatal("a file changed after verification reached the swap")
	}
}

func Test_a_build_without_a_release_version_does_not_update(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v9.0.0", nil)
	var hits atomic.Int32
	server := serve(t, &r, &hits)
	for _, current := range []string{"", "v1.0.0-3-gabcdef-dirty"} {
		h := new_harness(t, current, []ed25519.PublicKey{public}, server.URL, "")
		if _, err := h.updater.Check(context.Background()); !errors.Is(err, Err_not_a_release) {
			t.Fatalf("Check from version %q = %v, want Err_not_a_release", current, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("a build with no release version still contacted a channel")
	}
}

func Test_versions_compare_numerically(t *testing.T) {
	a, _ := Parse_version("v1.9.0")
	b, _ := Parse_version("1.10.0")
	if !a.Less(b) || b.Less(a) {
		t.Fatal("1.9.0 must be older than 1.10.0")
	}
	for _, bad := range []string{"1.2", "1.2.3.4", "v01.2.3", "1.2.x", "1.2.3-rc1"} {
		if _, ok := Parse_version(bad); ok {
			t.Fatalf("Parse_version(%q) accepted, want refused", bad)
		}
	}
}

func Test_the_compiled_keys_decode(t *testing.T) {
	keys, err := Trusted_keys()
	if err != nil || len(keys) != 2 {
		t.Fatalf("Trusted_keys = %d keys, %v; want the primary and the backup", len(keys), err)
	}
}

func Test_a_manifest_with_a_path_in_its_file_name_is_refused(t *testing.T) {
	_, private := new_key(t)
	for _, name := range []string{"../svpn-service.exe", `..\x.exe`, "a/b.exe", ".hidden", "update.json", "svpn-swap.exe", "SingularityVPN-setup-1.1.0.dll"} {
		r := make_release(t, private, "v1.1.0", func(m *Manifest) { m.Setup.File = name })
		if _, err := Parse(r.manifest); err == nil {
			t.Fatalf("Parse accepted setup file %q", name)
		}
	}
	r := make_release(t, private, "v1.1.0", func(m *Manifest) { m.Mirrors = []string{"http://plain.example"} })
	if _, err := Parse(r.manifest); err == nil {
		t.Fatal("Parse accepted a plain-http mirror")
	}
	_ = filepath.Separator
}

// Test_the_version_just_installed_is_not_applied_again: after a successful
// swap the new service starts with the staged entry still on disk if the
// swap could not clear it; applying it again would swap forever.
func Test_the_version_just_installed_is_not_applied_again(t *testing.T) {
	h := new_harness(t, "v1.1.0", nil, "", "")
	path := filepath.Join(h.dir, "SingularityVPN-setup-1.1.0.exe")
	os.WriteFile(path, []byte("x"), 0o600)
	sum, _ := File_sha256(path)
	if err := Save_state(h.dir, State{Staged: &Staged{Version: "v1.1.0", Path: path, Sha256: sum, Urgent: true}}); err != nil {
		t.Fatal(err)
	}
	if launched, err := h.updater.Maybe_apply(); launched || err != nil {
		t.Fatalf("Maybe_apply of the running version = %v, %v; want nothing", launched, err)
	}
	state := load(t, h.dir)
	if state.Staged != nil || len(h.applied) != 0 {
		t.Fatalf("staged = %+v, applied = %v; want the stale entry cleared and nothing applied", state.Staged, h.applied)
	}
}

// A swap may run for many minutes; another launched over it would fight it
// for the same files and the same service.
func Test_a_launched_swap_is_not_launched_again_while_it_may_be_running(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
	if _, err := h.updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if launched, err := h.updater.Maybe_apply(); !launched || err != nil {
		t.Fatalf("first Maybe_apply = %v, %v; want launched", launched, err)
	}
	h.now = h.now.Add(Swap_window - time.Minute)
	if launched, _ := h.updater.Maybe_apply(); launched {
		t.Fatal("a second swap was launched while the first may still be running")
	}
	h.now = h.now.Add(2 * time.Minute)
	if launched, _ := h.updater.Maybe_apply(); !launched {
		t.Fatal("a swap that never finished was not tried again after the window")
	}
}

func Test_a_manifest_older_than_this_build_does_not_touch_the_mirror_list(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", func(m *Manifest) { m.Mirrors = []string{"https://127.0.0.1:1/stale"} })
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.2.0", []ed25519.PublicKey{public}, server.URL, "")
	if _, err := h.updater.Check(context.Background()); err == nil {
		t.Fatal("a manifest older than the running build was accepted")
	}
	if state := load(t, h.dir); len(state.Mirrors) != 0 {
		t.Fatalf("mirrors = %v after a replayed old manifest; want none stored", state.Mirrors)
	}
}

func Test_an_unreadable_state_is_not_overwritten(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	server := serve(t, &r, nil)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, server.URL, "")
	// A directory where the file should be: it exists and cannot be read.
	if err := os.Mkdir(filepath.Join(h.dir, state_file), 0o700); err != nil {
		t.Fatal(err)
	}
	if staged, err := h.updater.Check(context.Background()); staged || err == nil {
		t.Fatalf("Check over an unreadable state = %v, %v; want an error", staged, err)
	}
	if err := Record_failure(h.dir, "v1.1.0", h.now); err == nil {
		t.Fatal("Record_failure over an unreadable state reported success")
	}
}
