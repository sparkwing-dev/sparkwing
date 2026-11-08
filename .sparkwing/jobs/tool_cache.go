package jobs

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func pipelineCacheDir(tool string) string {
	return filepath.Join(sparkwing.WorkDir(), ".sparkwing", "cache", tool)
}

// perf: the source key walked the 80,000-file cache inside the tree, so runs
// started a minute late; the git dir is outside that walk and goes with the
// worktree. One cache per worktree, because a shared one replays findings
// under another tree's paths, which the baseline filter then drops.
func lintCacheDir() string {
	if dir := worktreeGitDir(sparkwing.WorkDir()); dir != "" {
		return filepath.Join(dir, "sparkwing-cache", "golangci-lint")
	}
	return pipelineCacheDir("golangci-lint")
}

// safety: a linked worktree's .git is a file naming its private git directory;
// an unreadable or absent one leaves the cache in the tree, as before.
func worktreeGitDir(root string) string {
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	switch {
	case err != nil:
		return ""
	case info.IsDir():
		return dotGit
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok || dir == "" {
		return ""
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return dir
}
