package jobs

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// safety: the runner's resolver reads only engine.ShippedReleasesDirectory, so this path must match it.
const shippedEngineRoot = "usr/local/share/sparkwing/releases/"

const shippedEngineOrigin = "https://github.com/sparkwing-dev/sparkwing/releases/download/"

func shippedEngineFiles(arch string) []string {
	asset := "sparkwing-linux-" + arch
	return []string{"SHA256SUMS", "SHA256SUMS.sig", asset, asset + ".sig"}
}

// perf: signed policy selects the newest release for current SDKs, which is a build's release base until the next
// release publishes.
func shippedEngineVersion(build string) (string, bool) {
	base := build
	if module.IsPseudoVersion(build) {
		var err error
		if base, err = module.PseudoVersionBase(build); err != nil {
			return "", false
		}
	}
	return base, base != "" && semver.Canonical(base) == base && semver.Prerelease(base) == ""
}

// safety: nothing here grants trust; the runner verifies the release signatures before using any byte.
func fetchShippedEngine(ctx context.Context, fetch func(context.Context, string) ([]byte, error), version, dir string) (string, error) {
	release := filepath.Join(dir, version)
	if err := os.MkdirAll(release, 0o755); err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, arch := range releaseImageArches {
		for _, file := range shippedEngineFiles(arch) {
			if seen[file] {
				continue
			}
			seen[file] = true
			body, err := fetch(ctx, shippedEngineOrigin+version+"/"+file)
			if err != nil {
				return "", fmt.Errorf("fetch %s %s: %w", version, file, err)
			}
			if err := os.WriteFile(filepath.Join(release, file), body, 0o644); err != nil {
				return "", err
			}
		}
	}
	if _, err := checkShippedEngine(release); err != nil {
		return "", err
	}
	return release, nil
}

// safety: a torn or mismatched bundle fails here, before any image is pushed.
func checkShippedEngine(release string) (map[string]string, error) {
	version := filepath.Base(release)
	if semver.Canonical(version) != version || semver.Prerelease(version) != "" {
		return nil, fmt.Errorf("engine bundle directory %q is not named by a stable release version", release)
	}
	sums, err := readSHA256Sums(filepath.Join(release, "SHA256SUMS"))
	if err != nil {
		return nil, err
	}
	for _, arch := range releaseImageArches {
		for _, file := range shippedEngineFiles(arch) {
			if _, err := os.Stat(filepath.Join(release, file)); err != nil {
				return nil, fmt.Errorf("engine bundle: %w", err)
			}
		}
		if err := checkBinarySum(release, sums, "sparkwing", arch); err != nil {
			return nil, fmt.Errorf("engine bundle: %w", err)
		}
	}
	return sums, nil
}

func fetchHTTPS(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// safety: root-owned read-only files with zero times give a rerun the same layer digest,
// and the runner's unprivileged user cannot replace them.
func withShippedEngine(base v1.Image, release, arch string) (v1.Image, error) {
	dir := shippedEngineRoot + filepath.Base(release) + "/"
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range []string{"usr/", "usr/local/", "usr/local/share/", "usr/local/share/sparkwing/", shippedEngineRoot, dir} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			return nil, err
		}
	}
	for _, file := range shippedEngineFiles(arch) {
		body, err := os.ReadFile(filepath.Join(release, file))
		if err != nil {
			return nil, err
		}
		if err := tw.WriteHeader(&tar.Header{Name: dir + file, Typeflag: tar.TypeReg, Mode: 0o444, Size: int64(len(body))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	layerBytes := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(layerBytes)), nil
	})
	if err != nil {
		return nil, err
	}
	return mutate.AppendLayers(base, layer)
}
