package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// runGit executes a git subcommand inside dir (or cwd when empty)
// and returns stdout. stderr is surfaced into the returned error so
// callers get the actual git message, not a bare exit code.
func runGit(dir string, gitArgs ...string) (string, error) {
	cmd := exec.Command("git", gitArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s",
			strings.Join(gitArgs, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
