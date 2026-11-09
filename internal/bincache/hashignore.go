package bincache

import (
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// perf: one ls-files call avoids a git subprocess for each walked path.
func ignoredUnder(dir string) (dirs, files map[string]bool) {
	cmd := procenv.HostCommand("git", "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory") //nolint:forbidigo // git reads the developer's global excludes from HOME and XDG_CONFIG_HOME.
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
