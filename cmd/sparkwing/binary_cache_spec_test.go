package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func writeBinariesProfile(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	body := `profiles:
  shared-team:
    secrets: { type: env }
    state:   { type: sqlite, path: /tmp/state.db }
    logs:    { type: filesystem, path: /tmp/logs }
    cache:
      type: filesystem
      path: /tmp/cache
      binaries:
        type: s3
        bucket: sparkwing-binaries
        prefix: ci/
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles.yaml: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
	t.Setenv("SPARKWING_PROFILE", "shared-team")
}

// The compile path fetches bin/<hash> from the spec this returns, which is
// what makes cache.binaries isolation real rather than merely parsed.
func TestResolveBinaryCacheSpec_PrefersBinariesSubSpec(t *testing.T) {
	writeBinariesProfile(t)

	spec, _ := resolveBinaryCacheSpec("")
	if spec == nil {
		t.Fatal("resolveBinaryCacheSpec returned no spec")
	}
	if spec.Type != backends.TypeS3 || spec.Bucket != "sparkwing-binaries" || spec.Prefix != "ci/" {
		t.Fatalf("binaries spec = %+v, want the s3 sub-spec", spec)
	}
}

func TestResolveBinaryCacheSpec_FallsBackToCacheSurface(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	body := `profiles:
  plain:
    secrets: { type: env }
    state:   { type: sqlite, path: /tmp/state.db }
    logs:    { type: filesystem, path: /tmp/logs }
    cache:   { type: filesystem, path: /tmp/cache }
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles.yaml: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
	t.Setenv("SPARKWING_PROFILE", "plain")

	spec, _ := resolveBinaryCacheSpec("")
	if spec == nil {
		t.Fatal("resolveBinaryCacheSpec returned no spec")
	}
	if spec.Type != backends.TypeFilesystem || spec.Path != "/tmp/cache" {
		t.Fatalf("spec = %+v, want the cache surface", spec)
	}
}
