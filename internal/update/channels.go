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
)

// Channel is one place a release can be fetched from.
type Channel struct {
	Name      string
	manifest  string
	signature string
	artifact  func(file string) string
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
		artifact: func(file string) string {
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
		artifact:  func(file string) string { return base + "/" + url.PathEscape(file) },
	}
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
func download(ctx context.Context, client *http.Client, url string, want Artifact, path string) error {
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

	temp := path + ".part"
	file, err := os.Create(temp)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copy_err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, want.Size+1))
	close_err := file.Close()
	switch {
	case copy_err != nil:
		err = copy_err
	case close_err != nil:
		err = close_err
	case written != want.Size:
		err = fmt.Errorf("got %d bytes, the manifest says %d", written, want.Size)
	case hex.EncodeToString(hash.Sum(nil)) != want.Sha256:
		err = fmt.Errorf("sha256 does not match the manifest")
	}
	if err != nil {
		os.Remove(temp)
		return err
	}
	os.Remove(path)
	return os.Rename(temp, path)
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
