package sparkwing_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func useWorkDir(t *testing.T, dir string) {
	t.Helper()
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(dir)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
}

func toolCacheDir(t *testing.T, tool string) string {
	t.Helper()
	dir := sparkwing.ToolCacheDir(tool)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestToolCacheDir_DiffersBetweenWorktrees(t *testing.T) {
	first := t.TempDir()
	useWorkDir(t, first)
	a := toolCacheDir(t, "golangci-lint")

	second := t.TempDir()
	sparkwing.SetWorkDir(second)
	b := toolCacheDir(t, "golangci-lint")

	if a == b {
		t.Fatalf("worktrees %q and %q share cache dir %q", first, second, a)
	}
}

func TestToolCacheDir_DiffersWhenWorktreeNamesMatch(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "sparkwing", "release-prep")
	second := filepath.Join(root, "overwing", "release-prep")

	useWorkDir(t, first)
	a := toolCacheDir(t, "golangci-lint")
	sparkwing.SetWorkDir(second)
	b := toolCacheDir(t, "golangci-lint")

	if a == b {
		t.Fatalf("same-named worktrees share cache dir %q", a)
	}
}

func TestToolCacheDir_StableAcrossCalls(t *testing.T) {
	useWorkDir(t, t.TempDir())

	first := toolCacheDir(t, "golangci-lint")
	if second := toolCacheDir(t, "golangci-lint"); first != second {
		t.Fatalf("cache dir moved between calls: %q then %q", first, second)
	}
}

func TestToolCacheDir_DiffersBetweenTools(t *testing.T) {
	useWorkDir(t, t.TempDir())

	lint := toolCacheDir(t, "golangci-lint")
	if check := toolCacheDir(t, "staticcheck"); lint == check {
		t.Fatalf("tools share cache dir %q", lint)
	}
}

func TestToolCacheDir_CreatesTheDirectory(t *testing.T) {
	useWorkDir(t, t.TempDir())

	dir := toolCacheDir(t, "golangci-lint")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", dir)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe"), []byte("x"), 0o600); err != nil {
		t.Fatalf("cache dir not writable: %v", err)
	}
}

func TestToolCacheDir_ToolNameStaysInsideCacheRoot(t *testing.T) {
	useWorkDir(t, t.TempDir())

	root := filepath.Join(os.TempDir(), "sparkwing-toolcache")
	for _, tool := range []string{"../../escape", "..", "", "/etc/shadow", "."} {
		dir := toolCacheDir(t, tool)
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("tool %q: rel(%q, %q): %v", tool, root, dir, err)
		}
		if strings.HasPrefix(rel, "..") {
			t.Fatalf("tool %q escaped the cache root: %q", tool, dir)
		}
	}
}

func TestToolCacheDir_WithoutWorkDirFallsBackToCwd(t *testing.T) {
	useWorkDir(t, "")

	dir := toolCacheDir(t, "golangci-lint")
	if !filepath.IsAbs(dir) {
		t.Fatalf("cache dir is not absolute: %q", dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
}
