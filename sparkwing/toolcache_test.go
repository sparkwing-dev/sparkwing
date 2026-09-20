package sparkwing_test

import (
	"fmt"
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

	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	root := filepath.Join(home, "sparkwing-toolcache")
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

func TestToolCacheDir_SurvivesTemporaryDirectoryChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	useWorkDir(t, t.TempDir())
	firstShell := t.TempDir()
	t.Setenv("TMPDIR", firstShell)
	first := sparkwing.ToolCacheDir("golangci-lint")
	marker := filepath.Join(first, "cached-result")
	if err := os.WriteFile(marker, []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(firstShell); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	second := sparkwing.ToolCacheDir("golangci-lint")
	if first != second {
		t.Fatalf("cache moved between shells: %q -> %q", first, second)
	}
	if !strings.HasPrefix(second, home+string(filepath.Separator)) {
		t.Fatalf("cache outside Sparkwing home: %q", second)
	}
	if data, err := os.ReadFile(filepath.Join(second, "cached-result")); err != nil || string(data) != "result" {
		t.Fatalf("cache did not survive shell exit: %q, %v", data, err)
	}
}

func TestToolCacheDir_RefusesTheOperatorHomeFromATestBinary(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("no home directory, so this run cannot reach the refusal: %v", err)
	}
	operatorHome := filepath.Join(home, ".sparkwing")
	t.Setenv("SPARKWING_HOME", operatorHome)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("ToolCacheDir returned a path under the operator's home; a test binary must be refused one")
		}
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, "resolve tool cache home") {
			t.Errorf("panic names no failing step: %q", msg)
		}
		if !strings.Contains(msg, operatorHome) {
			t.Errorf("panic names no home, so the reader cannot tell which was refused: %q", msg)
		}
	}()
	_ = sparkwing.ToolCacheDir("golangci-lint")
}
