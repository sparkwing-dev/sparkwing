package orchestrator

import (
	"os"
	"path/filepath"
)

// repoShortName derives the short repo identity of the directory a run
// was launched from: the basename of the enclosing git toplevel, found
// by walking up to the first directory containing a .git entry (a
// directory for a normal checkout, a file for a linked worktree). Empty
// when dir is not inside a git repository.
func repoShortName(dir string) string {
	d := filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return filepath.Base(d)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// currentRepoShortName is repoShortName for the process working
// directory, the directory a local run is launched from.
func currentRepoShortName() string {
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
