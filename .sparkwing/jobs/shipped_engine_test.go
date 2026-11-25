package jobs

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func engineRelease(version string) map[string][]byte {
	files := map[string][]byte{}
	var sums strings.Builder
	for _, arch := range releaseImageArches {
		asset := "sparkwing-linux-" + arch
		files[asset] = []byte("engine " + version + " " + arch)
		files[asset+".sig"] = []byte("signature " + asset)
		sum := sha256.Sum256(files[asset])
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	}
	files["SHA256SUMS"] = []byte(sums.String())
	files["SHA256SUMS.sig"] = []byte("signature SHA256SUMS")
	return files
}

func writeEngineRelease(t *testing.T, version string, files map[string][]byte) string {
	t.Helper()
	release := filepath.Join(t.TempDir(), version)
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(release, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return release
}

func TestShippedEngineVersionIsTheStableReleaseBase(t *testing.T) {
	for build, want := range map[string]string{
		"v0.79.1-0.20261124233925-afe4fc22262e": "v0.79.0",
		"v0.79.0":                               "v0.79.0",
		"v0.68.0-qa.1":                          "",
		"v0.68.0-qa.1.0.20261106041046-718a7591f23a": "",
		"v0.0.0-20261106041046-718a7591f23a":         "",
		"v0.68.3-1-g0123456789ab":                    "",
	} {
		got, ok := shippedEngineVersion(build)
		if ok != (want != "") || got != want && ok {
			t.Errorf("shippedEngineVersion(%q) = %q, %t; want %q", build, got, ok, want)
		}
	}
}

func imageFile(t *testing.T, img v1.Image, path string) (*tar.Header, []byte) {
	t.Helper()
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for i := len(layers) - 1; i >= 0; i-- {
		rc, err := layers[i].Uncompressed()
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(rc)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if h.Name == path {
				body, err := io.ReadAll(tr)
				_ = rc.Close()
				if err != nil {
					t.Fatal(err)
				}
				return h, body
			}
		}
		_ = rc.Close()
	}
	return nil, nil
}

func TestPublishImagesShipsTheEngineReadOnlyInTheRunnerOnly(t *testing.T) {
	in, host := publishFixture(t, "amd64", "arm64")
	files := engineRelease("v0.79.0")
	in.Engine = writeEngineRelease(t, "v0.79.0", files)
	if _, err := publishReleaseImages(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	for _, arch := range releaseImageArches {
		runner, err := remote.Image(mustTag(t, host+"/sparkwing/sparkwing-runner:v0.68.0-qa.1-"+arch))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range shippedEngineFiles(arch) {
			h, body := imageFile(t, runner, shippedEngineRoot+"v0.79.0/"+file)
			if h == nil || string(body) != string(files[file]) {
				t.Fatalf("linux/%s runner does not ship %s", arch, file)
			}
			if h.Uid != 0 || h.FileInfo().Mode().Perm() != 0o444 {
				t.Fatalf("linux/%s %s is owned by %d with mode %v", arch, file, h.Uid, h.FileInfo().Mode())
			}
		}
		for _, other := range releaseImageArches {
			if h, _ := imageFile(t, runner, shippedEngineRoot+"v0.79.0/sparkwing-linux-"+other); other != arch && h != nil {
				t.Fatalf("linux/%s runner also ships the %s engine", arch, other)
			}
		}
		controller, err := remote.Image(mustTag(t, host+"/sparkwing/sparkwing-controller:v0.68.0-qa.1-"+arch))
		if err != nil {
			t.Fatal(err)
		}
		if h, _ := imageFile(t, controller, shippedEngineRoot+"v0.79.0/SHA256SUMS"); h != nil {
			t.Fatal("the controller image ships an engine")
		}
	}
}

func TestPublishImagesPushesNothingForAnEngineThatFailsItsSum(t *testing.T) {
	in, host := publishFixture(t, "amd64", "arm64")
	files := engineRelease("v0.79.0")
	files["sparkwing-linux-arm64"] = []byte("torn download")
	in.Engine = writeEngineRelease(t, "v0.79.0", files)
	if _, err := publishReleaseImages(t.Context(), in); err == nil {
		t.Fatal("published an engine bundle that differs from its SHA256SUMS")
	}
	if _, err := remote.Index(mustTag(t, host+"/sparkwing/sparkwing-runner:v0.68.0-qa.1")); err == nil {
		t.Fatal("pushed the runner image although its engine failed its sum")
	}
}

func TestDeployShipsTheReleaseBaseEngineOrDownloadsInstead(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(fmt.Sprintf("available=%t", available), func(t *testing.T) {
			f := newFakeDeploy(t)
			deployGitIn(t, f.repo, "tag", "v0.68.3")
			deployGitIn(t, f.repo, "commit", "--quiet", "--allow-empty", "-m", "main change")
			f.head = deployGitIn(t, f.repo, "rev-parse", "HEAD")
			deployGitIn(t, f.repo, "push", "--quiet", "origin", "HEAD:main")
			f.releases = map[string][]byte{}
			if available {
				for name, body := range engineRelease("v0.68.3") {
					f.releases[shippedEngineOrigin+"v0.68.3/"+name] = body
				}
			}
			if _, err := f.deployer(false).buildCandidate(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			if len(f.registry.published) != 1 {
				t.Fatalf("published %d times", len(f.registry.published))
			}
			engine := f.registry.published[0].Engine
			if !available {
				if engine != "" || !slices.ContainsFunc(f.said, func(msg string) bool { return strings.Contains(msg, "ships no engine") }) {
					t.Fatalf("an unavailable release shipped %q without saying so: %v", engine, f.said)
				}
				return
			}
			if filepath.Base(engine) != "v0.68.3" {
				t.Fatalf("shipped engine %q, want the release base v0.68.3", engine)
			}
			if _, err := checkShippedEngine(engine); err != nil {
				t.Fatal(err)
			}
		})
	}
}
