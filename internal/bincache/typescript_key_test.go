package bincache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTypeScriptCacheKeyNeedsNoGoModAndFollowsTheLockfile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"main":"pipeline.ts","dependencies":{"@sparkwing/sdk":"0.1.0"}}`)
	write("package-lock.json", `{"lockfileVersion":3}`)
	write("pipeline.ts", "")
	first, err := PipelineCacheKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if again, err := PipelineCacheKey(dir); err != nil || again != first {
		t.Fatalf("installing dependencies changed the key: %s -> %s, %v", first, again, err)
	}
	write("package-lock.json", `{"lockfileVersion":3,"packages":{}}`)
	if changed, err := PipelineCacheKey(dir); err != nil || changed == first {
		t.Fatalf("a lockfile change kept the key %s: %v", changed, err)
	}
}
