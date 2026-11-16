package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: publication runs unattended, so a hook, a credential prompt or a
// stalled remote must fail naming its git command, not run to the job deadline.
const (
	releaseGitRemoteTimeout = 5 * time.Minute
	releaseGitLocalTimeout  = 2 * time.Minute
)

var releaseGitBound = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

func releaseGit(ctx context.Context, dir string, args ...string) (string, error) {
	timeout := releaseGitLocalTimeout
	switch args[0] {
	case "push", "fetch", "ls-remote":
		timeout = releaseGitRemoteTimeout
	}
	command := "git " + strings.Join(args, " ")
	sparkwing.Info(ctx, "release: %s (in %s, timeout %s)", command, dir, timeout)
	bounded, cancel := releaseGitBound(ctx, timeout)
	defer cancel()
	res, err := sparkwing.Exec(bounded, "git", args...).Dir(dir).Env("GIT_TERMINAL_PROMPT", "0").Capture()
	if err == nil {
		return res.Stdout, nil
	}
	if ctx.Err() == nil && errors.Is(context.Cause(bounded), context.DeadlineExceeded) {
		return "", fmt.Errorf("release: %s in %s timed out after %s", command, dir, timeout)
	}
	if msg := strings.TrimSpace(res.Stderr); msg != "" {
		return "", fmt.Errorf("%s: %w: %s", command, err, msg)
	}
	return "", fmt.Errorf("%s: %w", command, err)
}

func releaseGitIn(dir string) gitRunner {
	return func(ctx context.Context, args ...string) (string, error) {
		out, err := releaseGit(ctx, dir, args...)
		return strings.TrimSpace(out), err
	}
}

// safety: a local tag origin lacks, on a commit no remote has, is what an
// interrupted cut leaves behind; the retry replaces it. Anything else is
// refused, and a tag already on origin never reaches here.
func staleReleaseTag(ctx context.Context, repo, version string) (bool, error) {
	target, err := releaseGit(ctx, repo, "for-each-ref", "--format=%(objectname)", "refs/tags/"+version)
	if err != nil || strings.TrimSpace(target) == "" {
		return false, err
	}
	commit, err := releaseGit(ctx, repo, "rev-parse", "refs/tags/"+version+"^{commit}")
	if err != nil {
		return false, err
	}
	commit = strings.TrimSpace(commit)
	remotes, err := releaseGit(ctx, repo, "for-each-ref", "--format=%(refname)", "--contains", commit, "refs/remotes/")
	if err != nil {
		return false, err
	}
	if remotes = strings.TrimSpace(remotes); remotes != "" {
		return false, fmt.Errorf("release: local tag %s points at %s, which %s already contains; it is not a leftover from an interrupted cut, so inspect it and delete it by hand: git -C %s tag -d %s",
			version, short(commit), strings.ReplaceAll(remotes, "\n", ", "), repo, version)
	}
	sparkwing.Info(ctx, "release: replacing local tag %s at %s; origin does not have it and no remote has its commit, so an interrupted cut left it", version, short(commit))
	return true, nil
}
