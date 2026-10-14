//go:build unix

package cluster

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestTriggerLoopStartsATypeScriptPipelineBehindTheBootstrap(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}
	sdk, err := filepath.Abs(filepath.Join("..", "..", "sdk", "typescript"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), ".sparkwing")
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "@sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sdk, filepath.Join(dir, "node_modules", "@sparkwing", "sdk")); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"package.json": `{"type":"module","main":"pipeline.ts","dependencies":{"@sparkwing/sdk":"file:` + sdk + `"}}`,
		"pipeline.ts":  "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := triggerBuildOrFetchBinary(t.Context(), dir, TriggerLoopOptions{}, "", "run-ts", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer got.release()
	if len(got.entry) != 4 || got.entry[1] != "--import" || got.entry[3] != filepath.Join(dir, "pipeline.ts") ||
		slices.Contains(got.entry, "") || got.cache != binaryCacheNone {
		t.Fatalf("entry = %q, cache %s", got.entry, got.cache)
	}
}
