package sparkwing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

const toolCacheRoot = "sparkwing-toolcache"

// ToolCacheDir returns a cache directory for an external tool, scoped to the
// worktree the pipeline is running in. Hand it to the tool through the tool's
// own cache environment variable:
//
//	sparkwing.Bash(ctx, "golangci-lint run ./...").
//		Env("GOLANGCI_LINT_CACHE", sparkwing.ToolCacheDir("golangci-lint")).
//		Run()
//
// The scope is per-worktree: a result that carries absolute paths replays
// another tree's paths when two worktrees share one directory.
//
// The cache lives under SPARKWING_HOME, by default ~/.sparkwing, so it outlives
// the shell. Test binaries take a temporary home; set SPARKWING_HOME explicitly
// when testing that a cache persists.
//
// It creates the directory if missing and panics if the Sparkwing home cannot
// be resolved.
func ToolCacheDir(tool string) string {
	scope := WorkDir()
	if scope == "" {
		if cwd, err := os.Getwd(); err == nil {
			scope = cwd
		}
	}
	sum := sha256.Sum256([]byte(scope))
	dir := filepath.Join(
		toolCacheHome(),
		depcache.Segment(tool, "tool"),
		depcache.Segment(filepath.Base(scope), "workdir")+"-"+hex.EncodeToString(sum[:6]),
	)
	_ = os.MkdirAll(dir, 0o700) //nolint:errcheck // the tool that needs the directory reports its own cache errors.
	return dir
}

func toolCacheHome() string {
	p, err := paths.DefaultPaths()
	if err != nil {
		panic(fmt.Sprintf("sparkwing: resolve tool cache home: %v", err))
	}
	return filepath.Join(p.Root, toolCacheRoot)
}
