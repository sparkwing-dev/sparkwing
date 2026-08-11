package bincache

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newCheckout lays out a pipeline module that replaces a sibling local
// module, the shape a developer gets from a repository checkout. root
// stands in for wherever that checkout happens to live.
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

// This is the property the cache key exists to provide: two checkouts
// of one commit, sitting at different paths, must agree. Before the
// key recorded replace targets by module path, the absolute location of
// ../lib entered the digest and every worktree compiled its own copy.
func TestPipelineCacheKey_PortableAcrossCheckoutPaths(t *testing.T) {
	a := newCheckout(t, t.TempDir())
	b := newCheckout(t, t.TempDir())

	keyA, keyB := mustKey(t, a), mustKey(t, b)
	if keyA != keyB {
		t.Fatalf("identical checkouts at different paths must share a key: %s (%s) vs %s (%s)", keyA, a, keyB, b)
	}
}

// Portability must not become blindness: a real edit inside the replace
// target still has to invalidate, wherever the checkout lives.
func TestPipelineCacheKey_PortableButStillContentSensitive(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	a, b := newCheckout(t, rootA), newCheckout(t, rootB)

	writeFile(t, filepath.Join(rootB, "lib", "lib.go"), "package lib\n\nfunc Answer() int { return 43 }\n")

	if keyA, keyB := mustKey(t, a), mustKey(t, b); keyA == keyB {
		t.Fatalf("a differing replace target must change the key; got %s twice", keyA)
	}
}

// A version-qualified replace is a different directive from a blanket
// one, so the two must not collide on a single label.
func TestPipelineCacheKey_ReplaceVersionIsPartOfIdentity(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	a, b := newCheckout(t, rootA), newCheckout(t, rootB)

	writeFile(t, filepath.Join(b, "go.mod"),
		"module example.com/pipeline\n\ngo 1.22\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib v1.2.3 => ../lib\n")

	if keyA, keyB := mustKey(t, a), mustKey(t, b); keyA == keyB {
		t.Fatalf("a version-qualified replace must not collide with a blanket one; got %s twice", keyA)
	}
}

func TestCompilePipeline_PassesTrimpath(t *testing.T) {
	log := installFakeGo(t)
	dir := newPipelineDir(t)
	if err := CompilePipeline(dir, filepath.Join(t.TempDir(), "bin", "pipelines")); err != nil {
		t.Fatalf("CompilePipeline: %v", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); !strings.Contains(got, "-trimpath") {
		t.Fatalf("expected -trimpath in the build argv, got: %q", got)
	}
}

// The end of the same story, through a real toolchain: with -trimpath
// the build directory stays out of the output, so two checkouts do not
// merely agree on a key, they produce the same bytes. Without it a
// shared key would hand one checkout a binary carrying the other's
// paths.
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
		if err := CompilePipeline(pipelineDir, dest); err != nil {
			t.Fatalf("CompilePipeline(%s): %v", pipelineDir, err)
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

	// GOFLAGS/GOWORK from the developer's shell would otherwise leak in.
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")

	a, b := newCheckout(t, t.TempDir()), newCheckout(t, t.TempDir())
	if sumA, sumB := sum(a), sum(b); sumA != sumB {
		t.Fatalf("identical checkouts must compile to identical bytes:\n  %s %s\n  %s %s", sumA, a, sumB, b)
	}
}

// The walk must not follow symlinks out of the tree it was asked
// about: a link pointing somewhere machine-specific would put foreign
// content into the key and break portability again.
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
