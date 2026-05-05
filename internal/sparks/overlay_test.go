package sparks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGoMod writes a minimal go.mod at path for tests. The module path
// is irrelevant to overlay generation; the require block is what matters.
func writeGoMod(t *testing.T, dir string, requires map[string]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("module example.com/consumer\n\ngo 1.25\n\n")
	if len(requires) > 0 {
		b.WriteString("require (\n")
		for mod, ver := range requires {
			b.WriteString("\t")
			b.WriteString(mod)
			b.WriteString(" ")
			b.WriteString(ver)
			b.WriteString("\n")
		}
		b.WriteString(")\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeGoBin installs a shell script named `go` in a temp dir and points
// SPARKS_GO_BIN at it so WriteOverlay can invoke `go mod download`
// without actually doing network I/O.
func fakeGoBin(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
# no-op; touch the sum file for the -modfile arg so behavior is realistic.
for arg in "$@"; do
  case "$arg" in
    -modfile=*)
      mod="${arg#-modfile=}"
      sum="${mod%.mod}.sum"
      : > "$sum"
      ;;
  esac
done
exit 0
`
	bin := filepath.Join(dir, "go")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKS_GO_BIN", bin)
}

func TestWriteOverlayWritesFiles(t *testing.T) {
	fakeGoBin(t)
	dir := t.TempDir()
	writeGoMod(t, dir, map[string]string{
		"github.com/sparkwing-dev/sparks-core": "v0.9.0",
	})
	goModBefore, _ := os.ReadFile(filepath.Join(dir, "go.mod"))

	changed, err := WriteOverlay(context.Background(), dir, map[string]string{
		"github.com/sparkwing-dev/sparks-core": "v0.10.3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected changed == true")
	}
	overlay, err := os.ReadFile(filepath.Join(dir, OverlayModfileName))
	if err != nil {
		t.Fatalf("overlay missing: %v", err)
	}
	if !strings.Contains(string(overlay), "sparks-core v0.10.3") {
		t.Fatalf("overlay did not contain resolved version:\n%s", overlay)
	}
	if _, err := os.Stat(filepath.Join(dir, OverlaySumfileName)); err != nil {
		t.Fatalf("sum missing: %v", err)
	}
	goModAfter, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if string(goModBefore) != string(goModAfter) {
		t.Fatal("go.mod was modified; hard rule violation")
	}
}

func TestWriteOverlayFastPath(t *testing.T) {
	fakeGoBin(t)
	dir := t.TempDir()
	writeGoMod(t, dir, map[string]string{
		"github.com/sparkwing-dev/sparks-core": "v0.9.0",
	})
	resolved := map[string]string{
		"github.com/sparkwing-dev/sparks-core": "v0.10.3",
	}
	// First call writes.
	if _, err := WriteOverlay(context.Background(), dir, resolved); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Second call: inputs unchanged, should be fast-path.
	changed, err := WriteOverlay(context.Background(), dir, resolved)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if changed {
		t.Fatal("expected changed == false on second call")
	}
}

func TestWriteOverlayAppendsMissingRequire(t *testing.T) {
	fakeGoBin(t)
	dir := t.TempDir()
	// go.mod has NO mention of sparks-core; overlay should append a
	// require line (this covers the ghost-pin / no-pin cases).
	writeGoMod(t, dir, nil)
	_, err := WriteOverlay(context.Background(), dir, map[string]string{
		"github.com/sparkwing-dev/sparks-core": "v0.10.3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	overlay, _ := os.ReadFile(filepath.Join(dir, OverlayModfileName))
	if !strings.Contains(string(overlay), "sparks-core v0.10.3") {
		t.Fatalf("overlay missing appended require:\n%s", overlay)
	}
}

func TestResolveAndWriteNoManifest(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, nil)
	changed, err := ResolveAndWrite(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Fatal("expected changed == false when no manifest")
	}
	if _, err := os.Stat(filepath.Join(dir, OverlayModfileName)); !os.IsNotExist(err) {
		t.Fatal("overlay should not exist when there is no manifest")
	}
}

func TestResolveAndWriteUpdatesStaleOverlay(t *testing.T) {
	fakeGoBin(t)
	dir := t.TempDir()
	writeGoMod(t, dir, map[string]string{
		"github.com/sparkwing-dev/sparks-core": "v0.9.0",
	})
	// Manifest pins to exact version v0.10.3; no network needed.
	manifest := `libraries:
  - name: sparks-core
    source: github.com/sparkwing-dev/sparks-core
    version: v0.10.3
`
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := ResolveAndWrite(context.Background(), dir)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !changed {
		t.Fatal("expected first call to change overlay")
	}

	// Rewrite manifest with a newer exact pin; resolution changes.
	manifest2 := `libraries:
  - name: sparks-core
    source: github.com/sparkwing-dev/sparks-core
    version: v0.11.0
`
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(manifest2), 0o644); err != nil {
		t.Fatal(err)
	}
	changed2, err := ResolveAndWrite(context.Background(), dir)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !changed2 {
		t.Fatal("expected second call to change overlay (different version)")
	}
	overlay, _ := os.ReadFile(filepath.Join(dir, OverlayModfileName))
	if !strings.Contains(string(overlay), "v0.11.0") {
		t.Fatalf("overlay did not update to v0.11.0:\n%s", overlay)
	}

	// Third call with unchanged manifest: fast path.
	changed3, err := ResolveAndWrite(context.Background(), dir)
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if changed3 {
		t.Fatal("expected third call to be a no-op (fast path)")
	}
}

func TestGitignoreAdds(t *testing.T) {
	fakeGoBin(t)
	// Simulate a repo root: sparkwingDir is a subdirectory named
	// ".sparkwing". Place a .git marker at the parent so locateGitignore
	// finds it.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sparkwingDir := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGoMod(t, sparkwingDir, nil)

	if _, err := WriteOverlay(context.Background(), sparkwingDir, map[string]string{
		"example.com/m": "v0.1.0",
	}); err != nil {
		t.Fatalf("overlay: %v", err)
	}
	gi, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("gitignore missing: %v", err)
	}
	if !strings.Contains(string(gi), ".sparkwing/.resolved.*") {
		t.Fatalf("gitignore missing entry:\n%s", gi)
	}

	// Second run must be idempotent (no duplicate line).
	if _, err := WriteOverlay(context.Background(), sparkwingDir, map[string]string{
		"example.com/m": "v0.1.0",
	}); err != nil {
		t.Fatalf("second overlay: %v", err)
	}
	gi2, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if strings.Count(string(gi2), ".sparkwing/.resolved.*") != 1 {
		t.Fatalf("gitignore entry duplicated:\n%s", gi2)
	}
}

func TestWriteOverlayRejectsEmptyDir(t *testing.T) {
	if _, err := WriteOverlay(context.Background(), "", nil); err == nil {
		t.Fatal("expected error for empty dir")
	}
}
