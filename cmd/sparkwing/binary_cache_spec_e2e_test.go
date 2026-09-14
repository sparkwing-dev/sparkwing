//go:build e2e

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The compile path fetches bin/<hash> from whatever resolveBinaryCacheSpec
// returns, so seeding only the binaries sub-spec proves cache.binaries
// isolation is real rather than merely parsed. The child process runs the
// fetched binary and reports its exit code back through cliError.
func TestCompileAndExec_FetchesTheBinaryFromTheBinariesSubSpec(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_BINARIES_CHILD") == "1" {
		err := compileAndExec(os.Getenv("SPARKWING_TEST_BINARIES_COMPILE_DIR"), nil,
			append(os.Environ(), "SPARKWING_FLEET=1", "GOWORK=off"), compileOptions{NoUpdate: true})
		var cliErr *cliError
		if !errors.As(err, &cliErr) {
			fmt.Fprintf(os.Stderr, "compileAndExec: %v\n", err)
			os.Exit(19)
		}
		os.Exit(cliErr.code)
	}

	pipelineDir := writeExitModule(t, "binariescompiled", 17)
	storeDir := filepath.Join(t.TempDir(), "binaries-store")
	seedArtifactStore(t, storeDir, pipelineDir, 23)

	profiles := filepath.Join(t.TempDir(), "profiles.yaml")
	body := fmt.Sprintf(`profiles:
  isolated-binaries:
    secrets: { type: env }
    state:   { type: sqlite, path: %s }
    logs:    { type: filesystem, path: %s }
    cache:
      type: filesystem
      path: %s
      binaries:
        type: filesystem
        path: %s
`, filepath.Join(t.TempDir(), "state.db"), filepath.Join(t.TempDir(), "logs"),
		filepath.Join(t.TempDir(), "plain-cache"), storeDir)
	if err := os.WriteFile(profiles, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles.yaml: %v", err)
	}

	code := runCompileChild(t, pipelineDir, profiles, "isolated-binaries")
	if code != 23 {
		t.Fatalf("exit code %d: the compile path did not run the binary seeded in cache.binaries (17 means it compiled locally)", code)
	}
}

// The sub-spec wins over the cache surface it sits under: the seeded store
// is the cache surface, the sub-spec points at an empty directory, and the
// compile must miss and build locally. Reading the cache surface instead
// would serve the seeded binary and exit 23.
func TestCompileAndExec_TheSubSpecOverridesTheCacheSurface(t *testing.T) {
	pipelineDir := writeExitModule(t, "binariesplain", 17)
	storeDir := filepath.Join(t.TempDir(), "binaries-store")
	seedArtifactStore(t, storeDir, pipelineDir, 23)

	profiles := filepath.Join(t.TempDir(), "profiles.yaml")
	body := fmt.Sprintf(`profiles:
  plain-cache:
    secrets: { type: env }
    state:   { type: sqlite, path: %s }
    logs:    { type: filesystem, path: %s }
    cache:
      type: filesystem
      path: %s
      binaries: { type: filesystem, path: %s }
`, filepath.Join(t.TempDir(), "state.db"), filepath.Join(t.TempDir(), "logs"),
		storeDir, filepath.Join(t.TempDir(), "empty-binaries"))
	if err := os.WriteFile(profiles, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles.yaml: %v", err)
	}

	code := runCompileChild(t, pipelineDir, profiles, "plain-cache")
	if code != 17 {
		t.Fatalf("exit code %d: the cache surface served the binary the sub-spec was meant to override", code)
	}
}
