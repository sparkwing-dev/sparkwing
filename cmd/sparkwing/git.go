package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func runGit(dir string, gitArgs ...string) (string, error) {
	return runGitContext(context.Background(), dir, gitArgs...)
}

func runGitContext(ctx context.Context, dir string, gitArgs ...string) (string, error) {
	cmd := procenv.HostCommandContext(ctx, "git", gitArgs...)
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
