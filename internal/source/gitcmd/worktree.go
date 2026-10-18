package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// HookWorktree creates an empty detached worktree for a hook's control and refusal commits.
func HookWorktree(ctx context.Context, repository, destination string) (func() error, error) {
	run := func(ctx context.Context, args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, args...)...)
		cmd.Env = os.Environ()
		if output, err := Output(ctx, cmd, true); err != nil {
			return fmt.Errorf("hook worktree failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	if err := run(ctx, "worktree", "add", "--detach", "--no-checkout", "--", destination); err != nil {
		return nil, err
	}
	return func() error {
		ctx := context.WithoutCancel(ctx)
		return errors.Join(run(ctx, "worktree", "remove", "--force", "--", destination), run(ctx, "worktree", "prune"))
	}, nil
}
