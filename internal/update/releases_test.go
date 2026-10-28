package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const test_repository = "/Singularity/VPN-App"

// code_host answers like GitHub's releases, over TLS -- a signed mirror
// must be https -- and latest is the release whose
// manifest it serves, and setups are kept per tag. A nil latest answers
// 404 to everything, as a blocked host effectively does.
type code_host struct {
	latest   *release
	tags     map[string]*release // tag -> release
	stall    bool                // send a few bytes of a setup, then nothing
	setups   atomic.Int32        // setup requests that reached it, found or not
	unstall  chan struct{}
	server   *httptest.Server
	releases string // the address to configure: <repository>/releases/latest/download
}

func new_code_host(t *testing.T, latest *release, tags map[string]*release) *code_host {
	t.Helper()
	h := &code_host{latest: latest, tags: tags, unstall: make(chan struct{})}
	h.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, test_repository)
		if h.latest == nil {
			http.NotFound(w, req)
			return
		}
		switch path {
		case "/releases/latest/download/manifest.json":
			w.Write(h.latest.manifest)
			return
		case "/releases/latest/download/manifest.json.sig":
			w.Write(h.latest.signature)
			return
		}
		rest, ok := strings.CutPrefix(path, "/releases/download/")
		if !ok {
			http.NotFound(w, req)
			return
		}
		h.setups.Add(1)
		tag, _, _ := strings.Cut(rest, "/")
		r := h.tags[tag]
		if r == nil {
			http.NotFound(w, req)
			return
		}
		if h.stall {
			w.Write(r.setup[:4])
			w.(http.Flusher).Flush()
			select {
			case <-h.unstall:
			case <-req.Context().Done():
			}
			return
		}
		w.Write(r.setup)
	}))
	t.Cleanup(h.server.Close)
	t.Cleanup(func() { close(h.unstall) })
	h.releases = h.server.URL + test_repository + releases_latest
	return h
}

// serve_plane answers like the control plane and counts setup downloads.
func serve_plane(t *testing.T, r *release, setups *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v1/app/update":
			w.Write(r.manifest)
		case "/api/v1/app/update.sig":
			w.Write(r.signature)
		case "/api/v1/app/download":
			setups.Add(1)
			w.Write(r.setup)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func Test_a_code_host_that_answered_serves_the_setup_instead_of_the_control_plane(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	host := new_code_host(t, &r, map[string]*release{"v1.1.0": &r})
	var plane_setups atomic.Int32
	plane := serve_plane(t, &r, &plane_setups)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, plane.URL, host.releases)
	h.updater.config.Http_client = host.server.Client()

	if staged, err := h.updater.Check(context.Background()); err != nil || !staged {
		t.Fatalf("Check = %v, %v; want staged", staged, err)
	}
	if host.setups.Load() != 1 || plane_setups.Load() != 0 {
		t.Fatalf("setup fetched %d times from the code host and %d from the control plane; want once, from the code host", host.setups.Load(), plane_setups.Load())
	}
}

func Test_a_code_host_named_in_the_signed_mirror_list_is_preferred_too(t *testing.T) {
	public, private := new_key(t)
	host := new_code_host(t, nil, nil)
	r := make_release(t, private, "v1.1.0", func(m *Manifest) { m.Mirrors = []string{host.releases} })
	host.latest, host.tags = &r, map[string]*release{"v1.1.0": &r}
	var plane_setups atomic.Int32
	plane := serve_plane(t, &r, &plane_setups)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, plane.URL, "")
	h.updater.config.Http_client = host.server.Client()

	// The first check learns the list; the host has not been asked yet,
	// so it has not shown it is reachable, and the control plane serves.
	if staged, err := h.updater.Check(context.Background()); err != nil || !staged {
		t.Fatalf("first Check = %v, %v; want staged", staged, err)
	}
	if plane_setups.Load() != 1 || host.setups.Load() != 0 {
		t.Fatalf("first check: %d from the control plane, %d from the code host; want the control plane", plane_setups.Load(), host.setups.Load())
	}

	r2 := make_release(t, private, "v1.2.0", func(m *Manifest) { m.Mirrors = []string{host.releases} })
	host.latest, host.tags["v1.2.0"] = &r2, &r2
	r = r2 // the control plane serves &r
	if staged, err := h.updater.Check(context.Background()); err != nil || !staged {
		t.Fatalf("second Check = %v, %v; want staged", staged, err)
	}
	if plane_setups.Load() != 1 || host.setups.Load() != 1 {
		t.Fatalf("after the second check: %d from the control plane, %d from the code host; want the code host this time", plane_setups.Load(), host.setups.Load())
	}
}

func Test_an_unreachable_code_host_leaves_the_setup_to_the_control_plane(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	host := new_code_host(t, nil, nil) // blocked: 404 to everything
	var plane_setups atomic.Int32
	plane := serve_plane(t, &r, &plane_setups)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, plane.URL, host.releases)
	h.updater.config.Http_client = host.server.Client()

	if staged, err := h.updater.Check(context.Background()); err != nil || !staged {
		t.Fatalf("Check = %v, %v; want staged", staged, err)
	}
	if plane_setups.Load() != 1 {
		t.Fatalf("setup fetched %d times from the control plane; want once", plane_setups.Load())
	}
}

func Test_a_code_host_still_on_an_older_release_is_not_asked_first(t *testing.T) {
	public, private := new_key(t)
	old := make_release(t, private, "v1.1.0", nil)
	r := make_release(t, private, "v1.2.0", nil)
	host := new_code_host(t, &old, map[string]*release{"v1.1.0": &old})
	var plane_setups atomic.Int32
	plane := serve_plane(t, &r, &plane_setups)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, plane.URL, host.releases)
	h.updater.config.Http_client = host.server.Client()

	if staged, err := h.updater.Check(context.Background()); err != nil || !staged {
		t.Fatalf("Check = %v, %v; want staged", staged, err)
	}
	if plane_setups.Load() != 1 || host.setups.Load() != 0 {
		t.Fatalf("%d from the control plane, %d from the code host; want the control plane only", plane_setups.Load(), host.setups.Load())
	}
	if s := load(t, h.dir).Staged; s == nil || s.Version != "v1.2.0" {
		t.Fatalf("staged = %+v; want v1.2.0", s)
	}
}

func Test_the_setup_comes_from_the_release_tagged_with_the_manifests_version(t *testing.T) {
	ch := channel_for("https://github.com/Singularity/VPN-App/releases/latest/download/")
	if ch.manifest != "https://github.com/Singularity/VPN-App/releases/latest/download/manifest.json" {
		t.Fatalf("manifest = %s", ch.manifest)
	}
	if got := ch.artifact("v1.2.0", "SingularityVPN-setup-1.2.0.exe"); got != "https://github.com/Singularity/VPN-App/releases/download/v1.2.0/SingularityVPN-setup-1.2.0.exe" {
		t.Fatalf("artifact = %s", got)
	}
	if c := channel_for("https://mirror.example/releases"); c.prefer {
		t.Fatal("a plain directory was read as a code host")
	}
}

func Test_a_starved_download_moves_on_to_the_next_channel(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	host := new_code_host(t, &r, map[string]*release{"v1.1.0": &r})
	host.stall = true
	var plane_setups atomic.Int32
	plane := serve_plane(t, &r, &plane_setups)

	dir := t.TempDir()
	u, err := New(Config{
		Control_planes:  fixed(plane.URL),
		Fallback:        host.releases,
		Current_version: "v1.0.0",
		Keys:            []ed25519.PublicKey{public},
		Dir:             dir,
		Idle:            func() bool { return true },
		Apply:           func(Staged) error { return nil },
		Stall_timeout:   200 * time.Millisecond,
		Http_client:     host.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	staged, err := u.Check(context.Background())
	if !staged || err != nil || time.Since(began) > 5*time.Second {
		t.Fatalf("Check past a starved download = %v, %v after %v; want staged promptly", staged, err, time.Since(began))
	}
	if host.setups.Load() != 1 || plane_setups.Load() != 1 {
		t.Fatalf("%d from the code host, %d from the control plane; want the host tried, the control plane serving", host.setups.Load(), plane_setups.Load())
	}
}

func Test_a_mirror_that_is_also_the_fallback_is_asked_once(t *testing.T) {
	h := new_harness(t, "v1.0.0", nil, "", "https://mirror.example/r")
	list := h.updater.channels(State{Mirrors: []string{"https://mirror.example/r/"}})
	if len(list) != 1 {
		t.Fatalf("channels = %d; want 1", len(list))
	}
}

// release_of signs a manifest for exactly these setup bytes.
func release_of(t *testing.T, private ed25519.PrivateKey, version string, setup []byte, released time.Time) release {
	t.Helper()
	r := make_release(t, private, version, func(m *Manifest) {
		sum := sha256.Sum256(setup)
		m.Released_at = released
		m.Setup.Sha256 = hex.EncodeToString(sum[:])
		m.Setup.Size = int64(len(setup))
	})
	r.setup = setup
	return r
}

func Test_a_slow_but_steady_download_is_not_cut(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	plane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v1/app/update":
			w.Write(r.manifest)
		case "/api/v1/app/update.sig":
			w.Write(r.signature)
		default:
			// One byte every 40 ms: the whole takes far longer than the
			// stall limit, no gap between bytes does.
			for _, b := range r.setup {
				w.Write([]byte{b})
				w.(http.Flusher).Flush()
				time.Sleep(40 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(plane.Close)
	u, err := New(Config{
		Control_planes:  fixed(plane.URL),
		Current_version: "v1.0.0",
		Keys:            []ed25519.PublicKey{public},
		Dir:             t.TempDir(),
		Idle:            func() bool { return true },
		Apply:           func(Staged) error { return nil },
		Stall_timeout:   200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if staged, err := u.Check(context.Background()); err != nil || !staged {
		t.Fatalf("Check over a slow, steady download = %v, %v; want staged", staged, err)
	}
}

func Test_a_preferred_host_with_the_wrong_bytes_falls_through_to_the_control_plane(t *testing.T) {
	public, private := new_key(t)
	r := make_release(t, private, "v1.1.0", nil)
	tampered := r
	tampered.setup = []byte("setup for v1.1.1") // same length, other bytes
	host := new_code_host(t, &r, map[string]*release{"v1.1.0": &tampered})
	var plane_setups atomic.Int32
	plane := serve_plane(t, &r, &plane_setups)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, plane.URL, host.releases)
	h.updater.config.Http_client = host.server.Client()

	if staged, err := h.updater.Check(context.Background()); err != nil || !staged {
		t.Fatalf("Check = %v, %v; want staged from the control plane", staged, err)
	}
	if host.setups.Load() != 1 || plane_setups.Load() != 1 {
		t.Fatalf("%d from the code host, %d from the control plane; want the host tried, then the control plane", host.setups.Load(), plane_setups.Load())
	}
	var m Manifest
	if err := json.Unmarshal(r.manifest, &m); err != nil {
		t.Fatal(err)
	}
	s := load(t, h.dir).Staged
	if s == nil || s.Sha256 != m.Setup.Sha256 {
		t.Fatalf("staged = %+v; want the manifest's setup", s)
	}
	if sum, err := File_sha256(s.Path); err != nil || sum != m.Setup.Sha256 {
		t.Fatalf("staged file hashes to %s, %v; want the manifest's", sum, err)
	}
	if _, err := os.Stat(s.Path + ".part"); !os.IsNotExist(err) {
		t.Fatalf("a partial download was left behind: %v", err)
	}
}

func Test_a_host_with_another_build_under_the_same_version_is_not_asked_first(t *testing.T) {
	public, private := new_key(t)
	first := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	old := release_of(t, private, "v1.1.0", []byte("first build of v1.1.0"), first)
	again := release_of(t, private, "v1.1.0", []byte("second build of v1.1.0"), first.Add(time.Hour))
	host := new_code_host(t, &old, map[string]*release{"v1.1.0": &old})
	var plane_setups atomic.Int32
	plane := serve_plane(t, &again, &plane_setups)
	h := new_harness(t, "v1.0.0", []ed25519.PublicKey{public}, plane.URL, host.releases)
	h.updater.config.Http_client = host.server.Client()

	if staged, err := h.updater.Check(context.Background()); err != nil || !staged {
		t.Fatalf("Check = %v, %v; want staged", staged, err)
	}
	if host.setups.Load() != 0 || plane_setups.Load() != 1 {
		t.Fatalf("%d from the code host, %d from the control plane; want the control plane's newer build only", host.setups.Load(), plane_setups.Load())
	}
}
