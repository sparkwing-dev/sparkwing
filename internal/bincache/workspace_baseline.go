package bincache

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// WorkspaceBaseline names the commit a working-tree snapshot was captured against and the
// remote-tracking ref that commit is reached by, so a checkout made from the snapshot resolves
// the same baseline the capturing checkout had. A pipeline step that scopes itself with
// `git merge-base origin/main HEAD` needs both halves: the ref name it asks for and an ancestry
// that reaches it.
type WorkspaceBaseline struct {
	// Ref is a short remote-tracking name such as "origin/main".
	Ref string

	// SHA is the commit the capturing checkout's HEAD shared with Ref.
	SHA string
}

const (
	// WorkspaceBaseRefEnvKey carries WorkspaceBaseline.Ref in a trigger's environment.
	WorkspaceBaseRefEnvKey = "SPARKWING_WORKTREE_BASE_REF"

	// WorkspaceBaseSHAEnvKey carries WorkspaceBaseline.SHA in a trigger's environment.
	WorkspaceBaseSHAEnvKey = "SPARKWING_WORKTREE_BASE_SHA"
)

// WorkspaceBaselineFromEnv reads the baseline a working-tree trigger recorded. It returns the
// zero value when either half is absent or malformed, which leaves the checkout as it was before
// baselines existed.
func WorkspaceBaselineFromEnv(env map[string]string) WorkspaceBaseline {
	baseline, err := WorkspaceBaseline{
		Ref: env[WorkspaceBaseRefEnvKey],
		SHA: env[WorkspaceBaseSHAEnvKey],
	}.Resolve()
	if err != nil {
		return WorkspaceBaseline{}
	}
	return baseline
}

// Env renders the baseline as the trigger environment keys a runner reads, and returns nil for a
// baseline that carries no usable ref and commit.
func (b WorkspaceBaseline) Env() map[string]string {
	resolved, err := b.Resolve()
	if err != nil {
		return nil
	}
	return map[string]string{
		WorkspaceBaseRefEnvKey: resolved.Ref,
		WorkspaceBaseSHAEnvKey: resolved.SHA,
	}
}

// Resolve reports the baseline in the form the runner uses, and an error when the ref is not a
// remote-tracking name or the commit is not an object id.
func (b WorkspaceBaseline) Resolve() (WorkspaceBaseline, error) {
	sha, err := validateGitObject(b.SHA)
	if err != nil {
		return WorkspaceBaseline{}, fmt.Errorf("working-tree baseline: %w", err)
	}
	ref := strings.TrimSpace(b.Ref)
	if err := validateRemoteTrackingRef(ref); err != nil {
		return WorkspaceBaseline{}, err
	}
	return WorkspaceBaseline{Ref: ref, SHA: sha}, nil
}

// safety: this name becomes a ref path and a git argument, so only a plain remote/branch passes.
var remoteTrackingRefRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)+$`)

func validateRemoteTrackingRef(ref string) error {
	if !remoteTrackingRefRE.MatchString(ref) || strings.Contains(ref, "..") {
		return fmt.Errorf("working-tree baseline: ref %q must name a remote-tracking branch such as origin/main", ref)
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("working-tree baseline: ref %q must name a remote-tracking branch such as origin/main", ref)
		}
	}
	return nil
}

func adoptWorkspaceBaseline(ctx context.Context, repoDir, gcURL, token, snapshotSHA string, baseline WorkspaceBaseline) error {
	resolved, err := baseline.Resolve()
	if err != nil {
		return err
	}
	if resolved.SHA == snapshotSHA {
		return fmt.Errorf("working-tree baseline: %s is the snapshot commit itself", resolved.SHA)
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = gitHTTPEnv(gcURL, token)
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), runErr, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("fetch", "--depth", "1", "--end-of-options", "origin", resolved.SHA); err != nil {
		return err
	}
	if err := run("update-ref", "refs/remotes/"+resolved.Ref, resolved.SHA); err != nil {
		return err
	}
	// safety: the snapshot commit is parentless, so the baseline reaches HEAD only as a graft.
	if err := run("replace", "--graft", snapshotSHA, resolved.SHA); err != nil {
		return err
	}
	// safety: a runner whose global config turns replacement off would read the graft as absent.
	if err := run("config", "core.useReplaceRefs", "true"); err != nil {
		return err
	}
	return completeShallowCommit(ctx, repoDir, snapshotSHA)
}

// hack: git stops a traversal at a shallow boundary even where a replacement supplies the
// parents, and no porcelain drops one boundary, so the file is rewritten. The snapshot commit
// has no parents of its own, so removing it loses no history.
func completeShallowCommit(ctx context.Context, repoDir, sha string) error {
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return fmt.Errorf("resolve git directory: %w", err)
	}
	path := filepath.Join(strings.TrimSpace(string(out)), "shallow")
	data, err := os.ReadFile(path) // #nosec G304 -- a path under the checkout this call just made
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read shallow boundary: %w", err)
	}
	kept := make([]string, 0, 4)
	for _, line := range strings.Fields(string(data)) {
		if !strings.EqualFold(line, sha) {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("clear shallow boundary: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("rewrite shallow boundary: %w", err)
	}
	return nil
}

func warnBaselineUnavailable(baseline WorkspaceBaseline, err error) {
	if err == nil {
		return
	}
	// safety: the checkout is complete without the baseline, so only the steps that scope
	// themselves to it are affected, and they report the missing ref themselves.
	slog.Default().Warn("working-tree baseline unavailable; a step that diffs against it cannot resolve it",
		"ref", baseline.Ref, "sha", baseline.SHA, "err", err)
}
