package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const publishImageTagsStub = `#!/usr/bin/env bash
set -uo pipefail
state="$STUB_STATE"
ref="${*: -1}"
name="${ref##*/}"
name="${name%%:*}"
name="${name%%@*}"
if [ "${1:-}" = "buildx" ] && [ "${2:-}" = "imagetools" ] && [ "${3:-}" = "inspect" ]; then
  if [ -f "$state/fail/$name" ]; then
    cat "$state/fail/$name" >&2
    exit 1
  fi
  if [ -f "$state/tags/$name" ]; then
    cat "$state/tags/$name"
    exit 0
  fi
  echo "ERROR: ${ref}: manifest unknown" >&2
  exit 1
fi
if [ "${1:-}" = "buildx" ] && [ "${2:-}" = "imagetools" ] && [ "${3:-}" = "create" ]; then
  printf '%s\n' "$*" >>"$state/creates"
  digest="${ref##*@}"
  if [ -f "$state/drift/$name" ]; then
    digest="$(cat "$state/drift/$name")"
  fi
  printf '%s\n' "$digest" >"$state/tags/$name"
  exit 0
fi
exit 0
`

var publishImageBinaries = []string{
	"sparkwing-controller",
	"sparkwing-runner",
	"sparkwing-cache",
	"sparkwing-logs",
	"sparkwing-web",
}

func scannedDigest(binary string) string {
	return fmt.Sprintf("sha256:%064x", len(binary)*7+1)
}

func otherDigest(binary string) string {
	return fmt.Sprintf("sha256:%064x", len(binary)*13+9999)
}

type publishImageTagsCase struct {
	name       string
	tag        string
	force      bool
	published  map[string]string
	inspectErr map[string]string
	drift      map[string]string
	wantFail   bool
	wantStderr []string
	wantCreate bool
	wantLatest bool
}

func TestPublishImageTagsGuardsPublishedDigests(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("publish-image-tags needs %s on PATH: %v", tool, err)
		}
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	script := filepath.Join(root, "bin", "publish-image-tags.sh")

	cases := []publishImageTagsCase{
		{
			name:       "absent tags are published",
			tag:        "v1.2.3",
			wantCreate: true,
			wantLatest: true,
		},
		{
			name:       "prerelease tags skip latest",
			tag:        "v1.2.3-rc.1",
			wantCreate: true,
		},
		{
			name:       "a tag already on the scanned digest is republished",
			tag:        "v1.2.3",
			published:  map[string]string{"sparkwing-runner": scannedDigest("sparkwing-runner")},
			wantCreate: true,
			wantLatest: true,
		},
		{
			name:      "a moved tag refuses before any tag is created",
			tag:       "v1.2.3",
			published: map[string]string{"sparkwing-runner": otherDigest("sparkwing-runner")},
			wantFail:  true,
			wantStderr: []string{
				"sparkwing-runner:v1.2.3 already points at",
				"no image tag was moved",
			},
		},
		{
			name:       "an unreadable registry answer fails closed",
			tag:        "v1.2.3",
			inspectErr: map[string]string{"sparkwing-cache": "ERROR: 429 Too Many Requests"},
			wantFail:   true,
			wantStderr: []string{
				"refusing to retag on an unreadable registry answer",
				"429 Too Many Requests",
			},
		},
		{
			name:       "force_retag moves a published tag",
			tag:        "v1.2.3",
			force:      true,
			published:  map[string]string{"sparkwing-runner": otherDigest("sparkwing-runner")},
			wantCreate: true,
			wantLatest: true,
		},
		{
			name:       "a tag that lands on other bytes fails the step",
			tag:        "v1.2.3",
			drift:      map[string]string{"sparkwing-logs": otherDigest("sparkwing-logs")},
			wantFail:   true,
			wantStderr: []string{"resolves to", "after the retag"},
			wantCreate: true,
			wantLatest: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			work := t.TempDir()
			state := filepath.Join(work, "registry")
			stdout, stderr, runErr := runPublishImageTags(t, script, work, state, tc)

			if tc.wantFail && runErr == nil {
				t.Fatalf("script succeeded; stdout=%q stderr=%q", stdout, stderr)
			}
			if !tc.wantFail && runErr != nil {
				t.Fatalf("script failed: %v; stdout=%q stderr=%q", runErr, stdout, stderr)
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr %q does not contain %q", stderr, want)
				}
			}

			creates := readLines(t, filepath.Join(state, "creates"))
			if !tc.wantCreate {
				if len(creates) != 0 {
					t.Fatalf("registry was mutated before the refusal: %v", creates)
				}
				return
			}
			if len(creates) != len(publishImageBinaries) {
				t.Fatalf("got %d creates, want %d: %v", len(creates), len(publishImageBinaries), creates)
			}
			latest := 0
			for _, line := range creates {
				if strings.Contains(line, ":latest") {
					latest++
				}
			}
			if tc.wantLatest && latest != len(publishImageBinaries) {
				t.Fatalf("release tag did not move latest: %v", creates)
			}
			if !tc.wantLatest && latest != 0 {
				t.Fatalf("prerelease tag moved latest: %v", creates)
			}
			if tc.wantFail {
				return
			}
			assertPublishedListing(t, filepath.Join(work, "out", "image-digests.json"), tc.tag)
		})
	}
}

func runPublishImageTags(t *testing.T, script, work, state string, tc publishImageTagsCase) (string, string, error) {
	t.Helper()
	binDir := filepath.Join(work, "bin")
	digestDir := filepath.Join(work, "scanned-image-digests")
	for _, dir := range []string{binDir, digestDir, filepath.Join(state, "tags"), filepath.Join(state, "fail"), filepath.Join(state, "drift")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(publishImageTagsStub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, binary := range publishImageBinaries {
		if err := os.WriteFile(filepath.Join(digestDir, binary), []byte(scannedDigest(binary)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, seed := range []struct {
		dir    string
		values map[string]string
	}{
		{"tags", tc.published},
		{"fail", tc.inspectErr},
		{"drift", tc.drift},
	} {
		for binary, value := range seed.values {
			if err := os.WriteFile(filepath.Join(state, seed.dir, binary), []byte(value+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	cmd := exec.Command("bash", script)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_STATE="+state,
		"TAG="+tc.tag,
		"FORCE_RETAG="+fmt.Sprint(tc.force),
		"DIGEST_DIR="+digestDir,
		"OUTPUT="+filepath.Join(work, "out", "image-digests.json"),
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func assertPublishedListing(t *testing.T, path, tag string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published listing: %v", err)
	}
	var listing struct {
		Tag    string `json:"tag"`
		Images []struct {
			Image  string `json:"image"`
			Tag    string `json:"tag"`
			Digest string `json:"digest"`
		} `json:"images"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		t.Fatalf("parse published listing: %v (%s)", err, body)
	}
	if listing.Tag != tag {
		t.Fatalf("listing tag = %q, want %q", listing.Tag, tag)
	}
	got := make([]string, 0, len(listing.Images))
	want := make([]string, 0, len(publishImageBinaries))
	for _, image := range listing.Images {
		if image.Tag != tag {
			t.Fatalf("listing entry %q carries tag %q", image.Image, image.Tag)
		}
		got = append(got, image.Image+" "+image.Digest)
	}
	for _, binary := range publishImageBinaries {
		want = append(want, "ghcr.io/sparkwing-dev/"+binary+" "+scannedDigest(binary))
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("listing = %v, want %v", got, want)
	}
}
