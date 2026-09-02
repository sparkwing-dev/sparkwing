package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("verify-release", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "release asset directory")
	verify := fs.Bool("verify", false, "verify existing signatures")
	public := fs.Bool("public-key", false, "print the public key derived from the signing seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	privateKey, err := releaseauth.PrivateKey(os.Getenv("SPARKWING_RELEASE_SIGNING_KEY"))
	if err != nil {
		return err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if !releaseauth.TrustedPublicKey(base64.StdEncoding.EncodeToString(publicKey)) {
		return errors.New("release signing key is not trusted by shipped updaters")
	}
	if *public {
		fmt.Println(base64.StdEncoding.EncodeToString(publicKey))
		return nil
	}
	return process(*dist, privateKey, publicKey, *verify)
}

const imageDigestsAsset = "image-digests.json"

func process(dist string, privateKey ed25519.PrivateKey, publicKey ed25519.PublicKey, verify bool) error {
	if err := validateReleaseAssets(dist); err != nil {
		return err
	}
	manifestPath := filepath.Join(dist, "SHA256SUMS")
	assets, err := filepath.Glob(filepath.Join(dist, "sparkwing-*"))
	if err != nil {
		return err
	}
	assets = unsignedAssets(assets)
	if len(assets) == 0 {
		return errors.New("no Sparkwing release assets")
	}
	sort.Strings(assets)
	paths := append([]string{manifestPath}, assets...)
	digests := filepath.Join(dist, imageDigestsAsset)
	// safety: a release that skipped image publication carries no listing, so sign it when present rather than require it.
	switch _, err := os.Stat(digests); {
	case err == nil:
		paths = append(paths, digests)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		signaturePath := path + ".sig"
		if verify {
			signature, err := os.ReadFile(signaturePath)
			if err != nil {
				return err
			}
			if err := releaseauth.Verify(publicKey, body, signature); err != nil {
				return fmt.Errorf("verify %s: %w", filepath.Base(path), err)
			}
			continue
		}
		signature, err := releaseauth.Sign(privateKey, body)
		if err != nil {
			return err
		}
		// #nosec G703 -- a release tool writing beside the assets the operator named
		if err := os.WriteFile(signaturePath, signature, 0o600); err != nil {
			return err
		}
	}
	if verify {
		if err := verifyManifestDigests(manifestPath, assets); err != nil {
			return err
		}
	}
	return nil
}

func verifyManifestDigests(manifestPath string, assets []string) error {
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	want := make(map[string]string, len(assets))
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return fmt.Errorf("malformed SHA256SUMS line %q", line)
		}
		if _, exists := want[fields[1]]; exists {
			return fmt.Errorf("duplicate SHA256SUMS entry %q", fields[1])
		}
		want[fields[1]] = strings.ToLower(fields[0])
	}
	for _, path := range assets {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(body))
		name := filepath.Base(path)
		if want[name] != digest {
			return fmt.Errorf("SHA256SUMS mismatch for %s", name)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		return fmt.Errorf("SHA256SUMS contains unexpected entries: %v", want)
	}
	return nil
}

func expectedReleaseAssets() []string {
	var names []string
	for _, binary := range []string{"sparkwing", "sparkwing-cache", "sparkwing-controller", "sparkwing-logs", "sparkwing-runner", "sparkwing-web"} {
		for _, goos := range []string{"darwin", "linux"} {
			for _, arch := range []string{"amd64", "arm64"} {
				names = append(names, binary+"-"+goos+"-"+arch)
			}
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		names = append(names, "sparkwing-windows-"+arch+".exe")
	}
	sort.Strings(names)
	return names
}

func validateReleaseAssets(dist string) error {
	paths, err := filepath.Glob(filepath.Join(dist, "sparkwing-*"))
	if err != nil {
		return err
	}
	paths = unsignedAssets(paths)
	got := make([]string, len(paths))
	for i, path := range paths {
		got[i] = filepath.Base(path)
	}
	sort.Strings(got)
	want := expectedReleaseAssets()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		return fmt.Errorf("release asset set mismatch: got %v want %v", got, want)
	}
	return nil
}

func unsignedAssets(paths []string) []string {
	result := paths[:0]
	for _, path := range paths {
		if !strings.HasSuffix(path, ".sig") {
			result = append(result, path)
		}
	}
	return result
}
