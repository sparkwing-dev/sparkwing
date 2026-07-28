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
// A new worktree therefore starts cold, and the obvious remedy is to
// seed it by copying a cache some other worktree already filled. That
// does not work. A stored issue carries the absolute path of the tree
// that produced it, so a copy replays those paths exactly as a shared
// directory does, and the seeded run reports a tree it never linted.
//
// Restricting the seed to a run that reported nothing does not rescue
// it. Exclusion rules and diff baselines are applied when results are
// reported, while the cache stores what the analyzers returned, so a
// run can print "0 issues" and still leave path-bearing issues behind.
// Seeding is only sound between trees at the same absolute path.
//
// Running two lint jobs at once is a different problem, and a scoped
// cache does not touch it. golangci-lint takes its parallel-runner
// lock on golangci-lint.lock in the OS temp directory, so every run on
// the box contends on one file no matter where GOLANGCI_LINT_CACHE
// points -- only TMPDIR moves it. By default a run that cannot take
// the lock retries for 5s and then exits "parallel golangci-lint is
// running", which a gate reports as a lint failure against a tree that
// is fine.
//
// Pass --allow-serial-runners so the run waits its turn instead. It
// waits on golangci-lint's own lock, so it queues behind every other
// golangci-lint on the box, including runs that know nothing about
// sparkwing. The flag waits indefinitely, so bound it with a context
// deadline, or one wedged linter pins every gate on the machine. A
// deadline that fires establishes that the step did not finish, never
// why: report it as could-not-run with the time waited, and keep the
// word contention for a run that printed "parallel golangci-lint is
// running":
//
//	sparkwing.Bash(ctx, "golangci-lint run --allow-serial-runners ./...").
//		Env("GOLANGCI_LINT_CACHE", sparkwing.ToolCacheDir("golangci-lint")).
//		Run()
//
// --allow-parallel-runners drops the lock instead of waiting on it,
// which lets N linters share the box's CPU and memory at once. Prefer
// it only where the machine has headroom to spare.
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
