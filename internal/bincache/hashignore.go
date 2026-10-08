package bincache

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
)

var hashAllFiles atomic.Bool

// SetHashAllFiles makes every cache key this process computes hash the
// files Git ignores too, which `sparkwing run --sw-hash-all-files` asks for.
func SetHashAllFiles(on bool) { hashAllFiles.Store(on) }

func ignoredUnder(dir string, candidates []string) map[string]bool {
	if len(candidates) == 0 || hashAllFiles.Load() {
		return nil
	}

	cmd := exec.Command("git", "check-ignore", "-z", "--stdin")
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
