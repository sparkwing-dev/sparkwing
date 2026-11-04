package bincache

import (
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// ignoredUnder names the untracked files git ignores under dir, and the wholly ignored
// directories it collapses, as paths joined to dir. Outside a repository nothing is ignored.
// perf: one ls-files call replaces piping every walked path to check-ignore, which
// took seconds per listing on a few thousand files.
func ignoredUnder(dir string) (dirs, files map[string]bool) {
	cmd := procenv.HostCommand("git", "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	dirs, files = map[string]bool{}, map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		if rel, ok := strings.CutSuffix(p, "/"); ok {
			dirs[filepath.Join(dir, filepath.FromSlash(rel))] = true
		} else {
			files[filepath.Join(dir, filepath.FromSlash(p))] = true
		}
	}
	return dirs, files
}
