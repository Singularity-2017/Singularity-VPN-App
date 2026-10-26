package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Channel is one place a release can be fetched from.
type Channel struct {
	Name      string
	manifest  string
	signature string
	artifact  func(version, file string) string

	// prefer marks a channel whose bandwidth is not ours to pay for: a
	// setup is fetched from it first whenever it answered the same check
	// with the chosen version.
	prefer bool
}

// Control_plane_channel is the control plane's own release endpoints. It
// is tried first: it is the one address the application must reach to be
// useful at all.
func Control_plane_channel(base string) Channel {
	base = strings.TrimRight(base, "/")
	return Channel{
		Name:      "control plane",
		manifest:  base + "/api/v1/app/update",
		signature: base + "/api/v1/app/update.sig",
		artifact: func(_, file string) string {
			return base + "/api/v1/app/download?file=" + url.QueryEscape(file)
		},
	}
}

// Mirror_channel is a plain directory of release files at base.
func Mirror_channel(base string) Channel {
	base = strings.TrimRight(base, "/")
	return Channel{
		Name:      "mirror " + base,
		manifest:  base + "/manifest.json",
		signature: base + "/manifest.json.sig",
		artifact:  func(_, file string) string { return base + "/" + url.PathEscape(file) },
	}
}

// releases_latest ends the address of a code host's latest release
// files, as GitHub lays them out: <repository>/releases/latest/download.
const releases_latest = "/releases/latest/download"

// releases_channel is a code host's releases at repository, as GitHub
// lays them out. The manifest and its signature come from the latest
// release; the setup comes from the release tagged with the manifest's own
// version, so a release published between the two fetches cannot answer
// with a setup the manifest does not describe. To a build that predates
// this channel the same address is a plain mirror, which works for as long
// as the latest release is the one it chose.
func releases_channel(repository string) Channel {
	base := repository + releases_latest
	return Channel{
		Name:      "releases " + repository,
		manifest:  base + "/manifest.json",
		signature: base + "/manifest.json.sig",
		artifact: func(version, file string) string {
			return repository + "/releases/download/" + url.PathEscape(version) + "/" + url.PathEscape(file)
		},
		prefer: true,
	}
}

// channel_for reads a mirror address: <repository>/releases/latest/download
// is a code host's releases, anything else a plain directory.
func channel_for(base string) Channel {
	base = strings.TrimRight(base, "/")
	if repository, ok := strings.CutSuffix(base, releases_latest); ok {
		return releases_channel(repository)
	}
	return Mirror_channel(base)
}

// get fetches url, refusing anything but 200 and anything longer than
// limit.
func get(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("longer than %d bytes", limit)
	}
	return data, nil
}

// download writes the artifact to path, enforcing its size while reading
// and its hash at the end; on any failure nothing is left at path.
//
// A transfer that delivers nothing for stall is dropped, so the next
// channel is tried within a minute rather than at the end of the overall
// deadline. That is what throttling by DPI usually looks like: the first
// few kilobytes arrive, then the connection is held open and starved.
func download(ctx context.Context, client *http.Client, url string, want Artifact, path string, stall time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	var stalled atomic.Bool
	watchdog := time.AfterFunc(stall, func() { stalled.Store(true); cancel() })
	defer watchdog.Stop()
	body := &progress_reader{r: resp.Body, progress: func() { watchdog.Reset(stall) }}

	temp := path + ".part"
	file, err := os.Create(temp)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copy_err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, want.Size+1))
	close_err := file.Close()
	complete := close_err == nil && written == want.Size && hex.EncodeToString(hash.Sum(nil)) == want.Sha256
	switch {
	case complete:
		// Every byte the manifest describes arrived and matches; a
		// connection that then hung instead of closing owes nothing more.
	case stalled.Load():
		err = fmt.Errorf("nothing arrived for %v after %d bytes", stall, written)
	case copy_err != nil:
		err = copy_err
	case close_err != nil:
		err = close_err
	case written != want.Size:
		err = fmt.Errorf("got %d bytes, the manifest says %d", written, want.Size)
	default:
		err = fmt.Errorf("sha256 does not match the manifest")
	}
	if err != nil {
		os.Remove(temp)
		return err
	}
	os.Remove(path)
	return os.Rename(temp, path)
}

// progress_reader calls progress whenever a read delivers something.
type progress_reader struct {
	r        io.Reader
	progress func()
}

func (p *progress_reader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.progress()
	}
	return n, err
}

// File_sha256 hashes a file already on disk.
func File_sha256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
