package bincache

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func ignoredUnder(dir string, candidates []string) map[string]bool {
	if len(candidates) == 0 {
		return nil
	}

	cmd := procenv.HostCommand("git", "check-ignore", "-z", "--stdin")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(strings.Join(candidates, "\x00") + "\x00")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {

		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return nil
		}
	}

	ignored := make(map[string]bool)
	for _, p := range strings.Split(out.String(), "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	return ignored
}

// perf: pruning wholly ignored directories keeps a large build cache inside the
// tree from being walked and handed to check-ignore path by path.
func ignoredDirsUnder(dir string) map[string]bool {
	cmd := procenv.HostCommand("git", "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	dirs := make(map[string]bool)
	for _, p := range strings.Split(string(out), "\x00") {
		if rel, ok := strings.CutSuffix(p, "/"); ok && rel != "" {
			dirs[filepath.Join(dir, filepath.FromSlash(rel))] = true
		}
	}
	return dirs
}
