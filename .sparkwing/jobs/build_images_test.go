package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestServiceBuildStampMatchesOnlyDescribeOfAStableTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"v0.68.6-0-g0123456789ab": true, "v0.68.6-14-g0123456789ab": true,
		"v0.68.6": false, "commit-0123456": false, "v0.68.6-qa.1-3-g0123456789ab": false,
		"v0.68.6-3-g0123456789ab-dirty": false, "v0.68.6-03-g0123456789ab": false, "v0.68.6-3-gABCDEF012345": false,
	} {
		if serviceBuildStamp.MatchString(tag) != want {
			t.Errorf("serviceBuildStamp(%q) = %v, want %v", tag, !want, want)
		}
	}
}

func TestResolveServiceBuildUsesCanonicalPseudoVersion(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"v0.69.2", "v0.69.3-0.20261106041046-718a7591f23a"},
		{"v0.69.3-qa.1", "v0.69.3-qa.1.0.20261106041046-718a7591f23a"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			version, err := resolveServiceBuild(t.Context(), func(_ context.Context, args ...string) (string, error) {
				switch strings.Join(args, " ") {
				case "rev-parse --is-shallow-repository":
					return "false", nil
				case "fetch --quiet --tags origin":
					return "", nil
				case "tag --merged HEAD --list v*":
					return tc.base + "\nv0.69.1\nv0.69.02\nv0.69.4-0.20261106041045-0123456789ab\n", nil
				case "show -s --format=%H%n%cI HEAD":
					return "718a7591f23a" + strings.Repeat("0", 28) + "\n2026-11-05T21:10:46-07:00", nil
				case "rev-parse refs/tags/" + tc.base + "^{commit}":
					return strings.Repeat("a", 40), nil
				default:
					return "", fmt.Errorf("unexpected git call: %v", args)
				}
			})
			if err != nil || version != tc.want {
				t.Fatalf("main version = %q, want %q: %v", version, tc.want, err)
			}
		})
	}
}

func TestBuildImagesSourceProvenance(t *testing.T) {
	const head = "238c02c076d69d9714a7877529f5fba4f062cd6c"
	const tag = "v0.70.1-0.20261108081304-238c02c076d6"
	for _, tc := range []struct {
		name, status, revision, wantError string
		push                              bool
	}{
		{name: "local", revision: head},
		{name: "push", revision: head, push: true},
		{name: "modified", status: " M source.go", revision: head, wantError: "uncommitted changes"},
		{name: "staged", status: "M  source.go", revision: head, wantError: "uncommitted changes"},
		{name: "untracked", status: "?? source.go", revision: head, wantError: "uncommitted changes"},
		{name: "short HEAD", revision: head[:12], wantError: "not a full commit id"},
		{name: "invalid HEAD", revision: strings.Repeat("z", 40), wantError: "not a full commit id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			log := filepath.Join(bin, "docker-args")
			t.Setenv("BUILD_IMAGES_TEST_STATUS", tc.status)
			t.Setenv("BUILD_IMAGES_TEST_HEAD", tc.revision)
			t.Setenv("BUILD_IMAGES_TEST_LOG", log)
			for name, body := range map[string]string{
				"git": `#!/bin/sh
case "$*" in
 'status --porcelain --untracked-files=normal') printf '%s\n' "$BUILD_IMAGES_TEST_STATUS" ;;
 'rev-parse HEAD') printf '%s\n' "$BUILD_IMAGES_TEST_HEAD" ;;
 *) exit 1 ;;
esac
`,
				"docker": `#!/bin/sh
printf '%s\n' "$@" >> "$BUILD_IMAGES_TEST_LOG"
`,
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			p := &BuildImages{args: BuildImagesArgs{Tag: tag, Registry: "registry.example", Push: tc.push}}
			ctx := newTestContext(t)
			err := p.resolveTag(ctx)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("resolveTag error = %v, want %q", err, tc.wantError)
				}
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("docker ran before provenance validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, component := range buildImagesComponents {
				if err := p.buildOne(ctx, component); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(strings.TrimSpace(string(raw)), "buildx\nbuild\n")
			if len(calls) != 3 {
				t.Fatalf("expected two image builds, got %q", raw)
			}
			for i, call := range calls[1:] {
				args := strings.Split(strings.TrimSpace(call), "\n")
				for _, pair := range [][2]string{
					{"--label", "org.opencontainers.image.revision=" + head},
					{"--label", "org.opencontainers.image.version=" + tag},
					{"--label", "org.opencontainers.image.source=https://github.com/sparkwing-dev/sparkwing"},
					{"--tag", "registry.example/" + buildImagesComponents[i].name + ":" + tag},
				} {
					at := slices.Index(args, pair[1])
					if at < 1 || args[at-1] != pair[0] {
						t.Errorf("build %d missing %v: %v", i, pair, args)
					}
				}
				if slices.Contains(args, "--push") != tc.push || slices.Contains(args, "--load") == tc.push {
					t.Errorf("wrong publication mode: %v", args)
				}
			}
		})
	}
}
