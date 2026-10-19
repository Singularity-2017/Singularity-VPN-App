// Command svpn-release makes the signing keys and signs a release.
//
//	svpn-release keygen <dir>
//	svpn-release sign --key <file> --setup <exe> --version vX.Y.Z --out <dir>
//	                  [--minimum vX.Y.Z] [--urgent] [--mirror https://...]...
//	svpn-release verify <dir>
//
// keygen writes the private keys with a DACL admitting only the account
// that ran it (see protect_windows.go; on Windows the mode bits alone do
// nothing) and prints only the public halves, which go into
// internal/update/keys.go. The backup key is written beside the primary;
// it is the owner's job to move it off this machine.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"singularity-vpn-app/internal/update"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		if len(os.Args) != 3 {
			usage()
			os.Exit(2)
		}
		err = keygen(os.Args[2])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		if len(os.Args) != 3 {
			usage()
			os.Exit(2)
		}
		err = verify(os.Args[2])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "svpn-release: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  svpn-release keygen <dir>")
	fmt.Fprintln(os.Stderr, "  svpn-release sign --key <file> --setup <exe> --version vX.Y.Z --out <dir> [--minimum vX.Y.Z] [--urgent] [--mirror URL]...")
	fmt.Fprintln(os.Stderr, "  svpn-release verify <dir>")
}

// keygen writes primary.key and backup.key into dir, refusing to overwrite
// either: replacing a key every installation trusts by accident would
// strand every installation on its current version.
func keygen(dir string) error {
	// protect replaces the directory's DACL, so it is only ever applied to
	// a directory keygen has just made or an empty one: pointed at C:\work
	// by mistake it would otherwise lock everyone else out of the tree.
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty; keygen writes keys only into a new or empty directory", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := protect(dir); err != nil {
		return fmt.Errorf("restricting %s to its owner: %w", dir, err)
	}
	for _, name := range []string{"primary", "backup"} {
		path := filepath.Join(dir, name+".key")
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; refusing to replace a signing key", path)
		}
	}
	for _, name := range []string{"primary", "backup"} {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, name+".key")
		if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(private)+"\n"), 0o600); err != nil {
			return err
		}
		if err := protect(path); err != nil {
			os.Remove(path)
			return fmt.Errorf("restricting %s to its owner: %w", path, err)
		}
		fmt.Printf("%s public key: %s\n", name, base64.StdEncoding.EncodeToString(public))
	}
	fmt.Println("Put both public keys into internal/update/keys.go. Move backup.key off this machine.")
	return nil
}

type mirrors []string

func (m *mirrors) String() string     { return strings.Join(*m, ",") }
func (m *mirrors) Set(v string) error { *m = append(*m, v); return nil }

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	key_path := fs.String("key", "", "private key file")
	setup := fs.String("setup", "", "the setup to publish")
	version := fs.String("version", "", "release version, vX.Y.Z")
	minimum := fs.String("minimum", "", "oldest version still allowed to wait for idle")
	urgent := fs.Bool("urgent", false, "apply even while a tunnel is up")
	out := fs.String("out", "", "directory to write the release into")
	var mirror_list mirrors
	fs.Var(&mirror_list, "mirror", "a mirror base URL; repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *key_path == "" || *setup == "" || *version == "" || *out == "" {
		return errors.New("sign needs --key, --setup, --version and --out")
	}
	if _, ok := update.Parse_version(*version); !ok {
		return fmt.Errorf("--version %q is not vX.Y.Z", *version)
	}
	private, err := read_key(*key_path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	file_name := "SingularityVPN-setup-" + strings.TrimPrefix(*version, "v") + ".exe"
	sum, size, err := copy_hashing(*setup, filepath.Join(*out, file_name))
	if err != nil {
		return err
	}
	manifest := update.Manifest{
		Schema:          update.Schema,
		Version:         *version,
		Released_at:     time.Now().UTC().Truncate(time.Second),
		Minimum_version: *minimum,
		Urgent:          *urgent,
		Setup:           update.Artifact{File: file_name, Sha256: sum, Size: size},
		Mirrors:         mirror_list,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	// The manifest must pass the same validation a client applies, or it
	// would be published and then refused everywhere.
	if _, err := update.Parse(raw); err != nil {
		return err
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw))
	if err := os.WriteFile(filepath.Join(*out, "manifest.json"), raw, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "manifest.json.sig"), []byte(signature+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed %s: %s (%d bytes, sha256 %s)\n", *version, file_name, size, sum)
	return check_trusted(raw, []byte(signature))
}

// verify checks a release directory the way a client will.
func verify(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	signature, err := os.ReadFile(filepath.Join(dir, "manifest.json.sig"))
	if err != nil {
		return err
	}
	keys, err := update.Trusted_keys()
	if err != nil {
		return err
	}
	if err := update.Verify(raw, signature, keys); err != nil {
		return err
	}
	manifest, err := update.Parse(raw)
	if err != nil {
		return err
	}
	sum, err := update.File_sha256(filepath.Join(dir, manifest.Setup.File))
	if err != nil {
		return err
	}
	if sum != manifest.Setup.Sha256 {
		return fmt.Errorf("%s does not match the manifest's sha256", manifest.Setup.File)
	}
	fmt.Printf("%s verifies against this build's keys\n", manifest.Version)
	return nil
}

// check_trusted warns when the key just used is not one this build of the
// client trusts: such a release would be refused by every installation.
func check_trusted(raw, signature []byte) error {
	keys, err := update.Trusted_keys()
	if err != nil {
		return err
	}
	if err := update.Verify(raw, signature, keys); err != nil {
		return fmt.Errorf("signed, but no key in internal/update/keys.go accepts it -- installations would refuse this release")
	}
	return nil
}

func read_key(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s is not an Ed25519 private key", path)
	}
	return ed25519.PrivateKey(raw), nil
}

func copy_hashing(from, to string) (string, int64, error) {
	in, err := os.Open(from)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, hash), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
