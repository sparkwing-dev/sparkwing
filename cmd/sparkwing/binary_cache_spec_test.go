package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
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

func writeExitModule(t *testing.T, module string, exit int) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.test/"+module+"\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"),
		fmt.Appendf(nil, "package main\nimport \"os\"\nfunc main() { os.Exit(%d) }\n", exit), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// seedArtifactStore puts a binary that exits with exitCode under the cache
// key pipelineDir hashes to, so a fetch is distinguishable from a compile.
func seedArtifactStore(t *testing.T, storeDir, pipelineDir string, exitCode int) {
	t.Helper()
	key, err := bincache.PipelineCacheKey(pipelineDir)
	if err != nil {
		t.Fatalf("cache key: %v", err)
	}
	src := writeExitModule(t, "binariesseeded", exitCode)
	binPath := filepath.Join(t.TempDir(), "seeded")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = src
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build seeded binary: %v\n%s", err, out)
	}

	ctx := context.Background()
	store, err := storeurl.OpenArtifactStoreFromSpec(ctx,
		backends.Spec{Type: backends.TypeFilesystem, Path: storeDir}, nil)
	if err != nil {
		t.Fatalf("open artifact store: %v", err)
	}
	if err := bincache.UploadToArtifactStore(ctx, store, key, binPath); err != nil {
		t.Fatalf("seed artifact store: %v", err)
	}
}

func runCompileChild(t *testing.T, pipelineDir, profiles, profileName string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCompileAndExec_FetchesTheBinaryFromTheBinariesSubSpec$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_TEST_BINARIES_CHILD=1",
		"SPARKWING_TEST_BINARIES_COMPILE_DIR="+pipelineDir,
		"SPARKWING_HOME="+filepath.Join(t.TempDir(), "home"),
		"SPARKWING_PROFILES="+profiles,
		"SPARKWING_PROFILE="+profileName,
		"GOWORK=off",
	)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil {
		t.Fatalf("child exited 0, expected the pipeline binary's own code\n%s", out)
	}
	if !errors.As(err, &exitErr) {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	if exitErr.ExitCode() == 19 {
		t.Fatalf("child did not reach the pipeline binary\n%s", out)
	}
	return exitErr.ExitCode()
}
