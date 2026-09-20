package sparkwing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		cacheSegment(tool, "tool"),
		cacheSegment(filepath.Base(scope), "workdir")+"-"+hex.EncodeToString(sum[:6]),
	)
	_ = os.MkdirAll(dir, 0o700) //nolint:errcheck // the tool that needs the directory reports its own cache errors.
	return dir
}

func toolCacheHome() string {
	p, err := paths.DefaultPaths()
	if err != nil {
		panic(fmt.Sprintf("sparkwing: resolve tool cache home: %v; set SPARKWING_HOME to a writable directory", err))
	}
	return filepath.Join(p.Root, toolCacheRoot)
}

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
