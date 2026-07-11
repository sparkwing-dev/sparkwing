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
