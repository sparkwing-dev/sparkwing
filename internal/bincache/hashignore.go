package bincache

import (
	"bytes"
	"errors"
	"os/exec"
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
