package cluster

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func writeTrivialPipeline(t *testing.T) string {
	t.Helper()
	pipelineDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pipelineDir, "go.mod"), []byte("module example.com/pipeline\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return pipelineDir
}

func TestTriggerBinaryWithoutStorageBuildsLocally(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CACHE_TOKEN", "retired-token")
	binary, err := triggerBuildOrFetchBinary(t.Context(), writeTrivialPipeline(t), nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer binary.release()
	if binary.cache != binaryCacheCompiled {
		t.Fatalf("binary cache = %q", binary.cache)
	}
	if _, err := os.Stat(binary.lease.Path()); err != nil {
		t.Fatal(err)
	}
}
