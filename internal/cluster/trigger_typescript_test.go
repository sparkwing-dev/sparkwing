//go:build unix

package cluster

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTriggerLoopPreparesATypeScriptPipelineEntry(t *testing.T) {
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
		"pipeline.ts":  `process.stdout.write(JSON.stringify(process.argv.slice(2)));`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := triggerBuildOrFetchBinary(t.Context(), dir, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer got.release()
	if len(got.entry) != 2 || got.entry[1] != filepath.Join(dir, "pipeline.ts") || got.cache != binaryCacheNone {
		t.Fatalf("entry = %q, cache %s", got.entry, got.cache)
	}
	output, err := got.entry.Exec().CombinedOutput()
	if err != nil {
		t.Fatalf("start TypeScript entry: %v\n%s", err, output)
	}
	if string(output) != `["--sw-node-protocol"]` {
		t.Fatalf("entry arguments = %s", output)
	}
}
