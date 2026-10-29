package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the base carries buildx's attestation entry, so the assembly must
// skip a platform that is not linux.
func pushBase(t *testing.T, host, repo string, arches ...string) string {
	t.Helper()
	index := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	for _, arch := range arches {
		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		cfg = cfg.DeepCopy()
		cfg.OS, cfg.Architecture = "linux", arch
		cfg.Config.Entrypoint = []string{"/usr/local/bin/" + repo}
		if img, err = mutate.ConfigFile(img, cfg); err != nil {
			t.Fatal(err)
		}
		index = mutate.AppendManifests(index, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}}})
	}
	attestation, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	index = mutate.AppendManifests(index, mutate.IndexAddendum{Add: attestation, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "unknown", Architecture: "unknown"}}})
	tag, err := name.NewTag(host + "/bases/" + repo + "-base:runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(tag, index); err != nil {
		t.Fatal(err)
	}
	digest, err := index.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return tag.Context().Name() + "@" + digest.String()
}

func signedDist(t *testing.T) string {
	t.Helper()
	dist := t.TempDir()
	var sums strings.Builder
	for _, binary := range []string{"sparkwing-controller", "sparkwing-runner"} {
		for _, arch := range releaseImageArches {
			file := binary + "-linux-" + arch
			body := []byte("binary " + file)
			if err := os.WriteFile(filepath.Join(dist, file), body, 0o755); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), file)
		}
	}
	if err := os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dist
}

func publishFixture(t *testing.T, arches ...string) (PublishImagesArgs, string) {
	t.Helper()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	return PublishImagesArgs{
		Dist:           signedDist(t),
		Version:        "v0.68.0-qa.1",
		Revision:       "abc123",
		Registry:       host + "/sparkwing/",
		ControllerBase: pushBase(t, host, "sparkwing-controller", arches...),
		RunnerBase:     pushBase(t, host, "sparkwing-runner", arches...),
	}, host
}

func TestPublishImagesJoinsBothArchitecturesUnderOneIndexPerTag(t *testing.T) {
	in, host := publishFixture(t, "amd64", "arm64")
	refs, err := publishReleaseImages(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("published %v", refs)
	}
	for _, binary := range []string{"sparkwing-controller", "sparkwing-runner"} {
		repo := host + "/sparkwing/" + binary
		index, err := remote.Index(mustTag(t, repo+":v0.68.0-qa.1"))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := index.IndexManifest()
		if err != nil {
			t.Fatal(err)
		}
		if len(manifest.Manifests) != 2 {
			t.Fatalf("%s index has %d manifests", binary, len(manifest.Manifests))
		}
		for _, arch := range releaseImageArches {
			img, err := remote.Image(mustTag(t, repo+":v0.68.0-qa.1-"+arch))
			if err != nil {
				t.Fatalf("%s-%s child is not tagged: %v", binary, arch, err)
			}
			cfg, err := img.ConfigFile()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Architecture != arch || cfg.Config.Labels["org.opencontainers.image.version"] != "v0.68.0-qa.1" ||
				cfg.Config.Entrypoint[0] != "/usr/local/bin/"+binary {
				t.Errorf("%s-%s config = %+v", binary, arch, cfg.Config)
			}
			got, err := imageFileSHA256(img, "usr/local/bin/"+binary)
			if err != nil {
				t.Fatal(err)
			}
			want := sha256.Sum256([]byte("binary " + binary + "-linux-" + arch))
			if got != hex.EncodeToString(want[:]) {
				t.Errorf("%s-%s carries the wrong binary", binary, arch)
			}
		}
	}
	again, err := publishReleaseImages(t.Context(), in)
	if err != nil || len(again) != 2 {
		t.Fatalf("rerun over the same inputs: %v %v", again, err)
	}
}

func TestPublishImagesRefusesABaseWithoutArm64(t *testing.T) {
	in, _ := publishFixture(t, "amd64")
	if _, err := publishReleaseImages(t.Context(), in); err == nil || !strings.Contains(err.Error(), "no linux/arm64 image") {
		t.Fatalf("published from an amd64-only base: %v", err)
	}
}

func TestPublishImagesPushesNothingForABinaryThatFailsItsSum(t *testing.T) {
	in, host := publishFixture(t, "amd64", "arm64")
	if err := os.WriteFile(filepath.Join(in.Dist, "sparkwing-runner-linux-arm64"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := publishReleaseImages(t.Context(), in); err == nil || !strings.Contains(err.Error(), "sparkwing-runner-linux-arm64 does not match SHA256SUMS") {
		t.Fatalf("published a tampered binary: %v", err)
	}
	if _, err := remote.Index(mustTag(t, host+"/sparkwing/sparkwing-controller:v0.68.0-qa.1")); err == nil {
		t.Fatal("the controller was pushed before every binary was checked")
	}
}

func TestPublishImagesNeedsEveryInput(t *testing.T) {
	if _, err := publishReleaseImages(t.Context(), PublishImagesArgs{Dist: "dist"}); err == nil || !strings.Contains(err.Error(), "--version is required") {
		t.Fatalf("plan accepted missing inputs: %v", err)
	}
}

func mustTag(t *testing.T, ref string) name.Tag {
	t.Helper()
	tag, err := name.NewTag(ref)
	if err != nil {
		t.Fatal(err)
	}
	return tag
}

func TestRuntimeBasesArePushedMultiArch(t *testing.T) {
	err := (&BuildImages{}).Plan(t.Context(), nil, BuildImagesArgs{RuntimeBase: true}, sparkwing.RunContext{})
	if err == nil || !strings.Contains(err.Error(), "--runtime-base requires --push") {
		t.Fatalf("a local single-arch runtime base was accepted: %v", err)
	}
}

func TestPublishImagesPlansOneInlineJob(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (&PublishImages{}).Plan(t.Context(), plan, PublishImagesArgs{
			Dist: "dist", Version: "v0.68.0", Revision: "abc", Registry: "reg",
			ControllerBase: "reg/c@sha256:" + strings.Repeat("0", 64), RunnerBase: "reg/r@sha256:" + strings.Repeat("0", 64),
		}, sparkwing.RunContext{})
	})
	if node := mustNode(t, plan, "publish"); !node.Inline {
		t.Fatal("publish must run inline")
	}
}
