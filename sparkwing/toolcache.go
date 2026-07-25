package sparkwing

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// toolCacheRoot is the directory under the OS temp dir that parents
// every cache ToolCacheDir hands out.
const toolCacheRoot = "sparkwing-toolcache"

// ToolCacheDir returns a cache directory for an external tool, scoped
// to the worktree the pipeline is running in. Hand it to the tool
// through the tool's own cache environment variable:
//
//	sparkwing.Bash(ctx, "golangci-lint run ./...").
//		Env("GOLANGCI_LINT_CACHE", sparkwing.ToolCacheDir("golangci-lint")).
//		Run()
//
// A tool that keys its cache on file content alone -- golangci-lint
// among them -- replays a stored result for identical input no matter
// which checkout produced it. With one shared cache that means a run
// in one worktree can be served a sibling worktree's result and report
// that worktree's file paths, including paths from a worktree that has
// since been deleted. A per-worktree directory removes that.
//
// The path is derived from the absolute [WorkDir], so it stays stable
// across runs in one worktree -- the cache still earns its keep --
// and is disjoint between worktrees, including two worktrees whose
// leaf directory names match. The directory is created if missing; a
// creation failure is left to surface from the tool, which is what
// needs the directory and reports its own cache errors.
//
// Running two lint jobs at once is a separate problem this does not
// solve. golangci-lint takes its parallel-runner lock on a file in the
// OS temp directory, wherever GOLANGCI_LINT_CACHE points, so a second
// run started while another holds that lock still exits with "parallel
// golangci-lint is running". Pass --allow-parallel-runners, or
// serialize the jobs behind a lock of your own.
func ToolCacheDir(tool string) string {
	scope := WorkDir()
	if scope == "" {
		if cwd, err := os.Getwd(); err == nil {
			scope = cwd
		}
	}
	sum := sha256.Sum256([]byte(scope))
	dir := filepath.Join(
		os.TempDir(),
		toolCacheRoot,
		cacheSegment(tool, "tool"),
		cacheSegment(filepath.Base(scope), "workdir")+"-"+hex.EncodeToString(sum[:6]),
	)
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// cacheSegment reduces s to a single filesystem-safe path segment, so
// a tool name or worktree name carrying separators or dots cannot
// place the cache outside the toolCacheRoot tree.
func cacheSegment(s, fallback string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
	if safe = strings.Trim(safe, "-"); safe == "" {
		return fallback
	}
	return safe
}
