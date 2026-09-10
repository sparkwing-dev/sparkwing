package bincache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newCheckout(t *testing.T, root string) (pipelineDir string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "lib", "go.mod"), "module example.com/lib\n\ngo 1.22\n")
	writeFile(t, filepath.Join(root, "lib", "lib.go"), "package lib\n\nfunc Answer() int { return 42 }\n")

	pipelineDir = filepath.Join(root, ".sparkwing")
	writeFile(t, filepath.Join(pipelineDir, "go.mod"),
		"module example.com/pipeline\n\ngo 1.22\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n")
	writeFile(t, filepath.Join(pipelineDir, "main.go"),
		"package main\n\nimport \"example.com/lib\"\n\nfunc main() { _ = lib.Answer() }\n")
	return pipelineDir
}

func TestPipelineCacheKey_PortableAcrossCheckoutPaths(t *testing.T) {
	a := newCheckout(t, t.TempDir())
	b := newCheckout(t, t.TempDir())

	keyA, keyB := mustKey(t, a), mustKey(t, b)
	if keyA != keyB {
		t.Fatalf("identical checkouts at different paths must share a key: %s (%s) vs %s (%s)", keyA, a, keyB, b)
	}
}

func TestPipelineCacheKey_PortableButStillContentSensitive(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	a, b := newCheckout(t, rootA), newCheckout(t, rootB)

	writeFile(t, filepath.Join(rootB, "lib", "lib.go"), "package lib\n\nfunc Answer() int { return 43 }\n")

	if keyA, keyB := mustKey(t, a), mustKey(t, b); keyA == keyB {
		t.Fatalf("a differing replace target must change the key; got %s twice", keyA)
	}
}

func TestPipelineCacheKey_ReplaceVersionIsPartOfIdentity(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	a, b := newCheckout(t, rootA), newCheckout(t, rootB)

	writeFile(t, filepath.Join(b, "go.mod"),
		"module example.com/pipeline\n\ngo 1.22\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib v1.2.3 => ../lib\n")

	if keyA, keyB := mustKey(t, a), mustKey(t, b); keyA == keyB {
		t.Fatalf("a version-qualified replace must not collide with a blanket one; got %s twice", keyA)
	}
}

func TestCompilePipeline_PassesTrimpathAndStrips(t *testing.T) {
	log := installFakeGo(t)
	dir := newPipelineDir(t)
	if err := CompilePipeline(context.Background(), dir, filepath.Join(t.TempDir(), "bin", "pipelines")); err != nil {
		t.Fatalf("CompilePipeline: %v", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	got := strings.TrimSpace(string(raw))
	for _, want := range []string{"-trimpath", "-ldflags -s -w"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %s in the build argv, got: %q", want, got)
		}
	}
}

// The cache stores no digest of the binary it serves, so a build flag the key
// does not name hands every local and shared cache a stale binary that nothing
// downstream can detect.
func TestCompilePipeline_BuildFlagsAreNamedInTheCacheKey(t *testing.T) {
	log := installFakeGo(t)
	dir := newPipelineDir(t)
	if err := CompilePipeline(context.Background(), dir, filepath.Join(t.TempDir(), "bin", "pipelines")); err != nil {
		t.Fatalf("CompilePipeline: %v", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	want := strings.Join(compiledFlags(t, strings.Fields(strings.TrimSpace(string(raw)))), " ")

	_, parts, err := ExplainCacheKey(dir)
	if err != nil {
		t.Fatalf("ExplainCacheKey: %v", err)
	}
	for _, p := range parts {
		if p.Detail == want {
			return
		}
	}
	var labels []string
	for _, p := range parts {
		labels = append(labels, p.Label+"="+p.Detail)
	}
	t.Fatalf("no cache-key input names the build flags %q; inputs were %s", want, strings.Join(labels, ", "))
}

// A key that does not fold the flags into its hash serves the binary the old
// flags produced, whatever the parts list displays.
func TestPipelineCacheKey_ChangesWithBuildFlags(t *testing.T) {
	dir := newCheckout(t, t.TempDir())
	before := mustKey(t, dir)

	original := pipelineBuildFlags
	t.Cleanup(func() { pipelineBuildFlags = original })
	pipelineBuildFlags = append(append([]string{}, original...), "-tags", "probe")

	if after := mustKey(t, dir); after == before {
		t.Fatalf("changing the build flags left the key at %s", before)
	}
}

// compiledFlags returns the flags CompilePipeline passed to `go build`, minus
// -modfile, whose target the .resolved.mod input already hashes by content.
func compiledFlags(t *testing.T, argv []string) []string {
	t.Helper()
	var flags []string
	for i, a := range argv {
		if i == 0 && a == "build" {
			continue
		}
		if a == "-o" {
			return flags
		}
		if strings.HasPrefix(a, "-modfile=") {
			continue
		}
		flags = append(flags, a)
	}
	t.Fatalf("build argv has no -o: %v", argv)
	return nil
}

func TestCompilePipeline_IdenticalBinariesAcrossCheckoutPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles with the real toolchain")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}

	sum := func(pipelineDir string) string {
		t.Helper()
		dest := filepath.Join(t.TempDir(), "pipelines")
		if err := CompilePipeline(context.Background(), pipelineDir, dest); err != nil {
			t.Fatalf("CompilePipeline(context.Background(), %s): %v", pipelineDir, err)
		}
		f, err := os.Open(dest)
		if err != nil {
			t.Fatalf("open %s: %v", dest, err)
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			t.Fatalf("hash %s: %v", dest, err)
		}
		return hex.EncodeToString(h.Sum(nil))
	}

	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")

	a, b := newCheckout(t, t.TempDir()), newCheckout(t, t.TempDir())
	if sumA, sumB := sum(a), sum(b); sumA != sumB {
		t.Fatalf("identical checkouts must compile to identical bytes:\n  %s %s\n  %s %s", sumA, a, sumB, b)
	}
}

func TestWalkHashable_DoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.go"), "package outside\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	files, err := walkHashable(root, allFiles)
	if err != nil {
		t.Fatalf("walkHashable: %v", err)
	}
	for _, f := range files {
		if strings.Contains(f, "secret.go") {
			t.Fatalf("walk followed a symlink out of the tree: %v", files)
		}
	}
}
