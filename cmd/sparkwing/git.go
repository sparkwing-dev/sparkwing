package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

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
