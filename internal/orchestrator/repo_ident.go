package orchestrator

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// repoShortName derives the short repo identity of the directory a run
// was launched from: the basename of the repository the enclosing git
// toplevel belongs to, found by walking up to the first directory
// containing a .git entry. A .git directory is a normal checkout, so the
// toplevel's own basename is the repo. A .git file is a linked worktree
// or a submodule; a worktree resolves to the repository it was branched
// from, because a worktree is one branch of a repo and not a repo of its
// own. Empty when dir is not inside a git repository.
func repoShortName(dir string) string {
	d := filepath.Clean(dir)
	for {
		gitPath := filepath.Join(d, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if !info.IsDir() {
				if main := worktreeRepoDir(gitPath, d); main != "" {
					return filepath.Base(main)
				}
			}
			return filepath.Base(d)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// worktreeRepoDir resolves the working directory of the repository a
// linked worktree belongs to, from the "gitdir:" pointer in the
// worktree's .git file. Git writes that pointer as
// <common>/worktrees/<name>, where common is the repo's .git directory
// (or the bare repo itself), so the repo is one or two levels up. It
// returns "" for any other .git file -- a submodule's pointer has no
// worktrees segment, and a submodule is its own repo for pricing.
func worktreeRepoDir(gitFile, worktreeDir string) string {
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	gitDir := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:"); ok {
			gitDir = strings.TrimSpace(rest)
			break
		}
	}
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreeDir, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	if filepath.Base(filepath.Dir(gitDir)) != "worktrees" {
		return ""
	}
	common := filepath.Dir(filepath.Dir(gitDir))
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return strings.TrimSuffix(common, ".git")
}

// currentRepoShortName is repoShortName for the run's configured working
// directory. Node code may change the process-wide cwd while a run is active,
// but every profile read and write must retain the repository that launched
// the run. Calls outside a configured runtime fall back to the process cwd.
func currentRepoShortName() string {
	if wd := sparkwing.CurrentRuntime().WorkDir; wd != "" {
		return repoShortName(wd)
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return repoShortName(wd)
}

// scopedProfileKey is the identity a pipeline's capacity profile is stored
// under in the machine-global state database: repo-scoped, because pipeline
// names repeat across repos (every scaffolded repo ships a "ci") and pooling
// their samples and contended floors lets contention in one repo poison
// another's pricing. A run outside any git repo keeps the bare pipeline name.
// Every linked worktree of a repo shares the repo's key, because a pipeline
// costs what it costs whichever branch runs it; keying per worktree threw the
// learning away every time a ticket got a fresh branch, so every gate started
// from the conservative cold-start default.
func scopedProfileKey(repo, pipeline string) string {
	if repo == "" || pipeline == "" {
		return pipeline
	}
	return repo + "/" + pipeline
}

// currentProfileKey scopes a pipeline's profile identity to the repo the
// process runs from. Every profile read and write in one run goes through
// this, so pricing, folds, and contention tallies always land on one row.
func currentProfileKey(pipeline string) string {
	return scopedProfileKey(currentRepoShortName(), pipeline)
}
