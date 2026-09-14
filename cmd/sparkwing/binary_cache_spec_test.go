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
