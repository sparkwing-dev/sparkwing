package bincache

import (
	"context"
	"errors"
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

// SourceBundleObjectEnvKey binds a working-tree trigger to its one-run source object.
const SourceBundleObjectEnvKey = "SPARKWING_SOURCE_BUNDLE_OBJECT"

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

var gitObjectRE = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

func validateGitObject(sha string) (string, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if !gitObjectRE.MatchString(sha) {
		return "", fmt.Errorf("git sha must be a 40-64 character hex object id")
	}
	return sha, nil
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

// ErrBaselineUnservable means the snapshot names no repository to fetch its baseline from.
var ErrBaselineUnservable = errors.New("working-tree baseline: no repository can serve the baseline")

// AdoptWorkspaceBaseline gives a snapshot checkout the remote-tracking ref its capture was measured
// against. Its commit comes through the same direct credential fence as the run's source.
func AdoptWorkspaceBaseline(ctx context.Context, src RunSource, checkoutDir, snapshotSHA string, baseline WorkspaceBaseline, logger *slog.Logger) error {
	resolved, err := baseline.Resolve()
	if err != nil {
		return err
	}
	if resolved.SHA == snapshotSHA {
		return fmt.Errorf("working-tree baseline: %s is the snapshot commit itself", resolved.SHA)
	}
	snapshot, err := isWorkspaceSnapshotCommit(ctx, checkoutDir, snapshotSHA)
	if err != nil {
		return err
	}
	if !snapshot {
		return fmt.Errorf("working-tree baseline: %s is not a working-tree snapshot commit", snapshotSHA)
	}
	if src.RepoURL == "" {
		return ErrBaselineUnservable
	}
	baselineWork, err := os.MkdirTemp(filepath.Dir(checkoutDir), ".baseline-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(baselineWork) }()
	cred, err := runSourceCredential(ctx, src, logger)
	if err != nil {
		return err
	}
	opts := defaultDirectOptions()
	opts.cred, opts.keyTmpfsOnly = cred, !src.OwnerCredentials
	baselineDir, err := fetchRepositorySourceDirect(ctx, src.RepoURL, "", resolved.SHA, baselineWork, opts)
	if err != nil {
		return err
	}
	return adoptBaselineFromCheckout(ctx, checkoutDir, baselineDir, snapshotSHA, resolved)
}

func adoptBaselineFromCheckout(ctx context.Context, checkoutDir, baselineDir, snapshotSHA string, baseline WorkspaceBaseline) error {
	env := append(directLocalGitEnv(directGitEnv(os.Environ())), "GIT_ALLOW_PROTOCOL=file", "GIT_TERMINAL_PROMPT=0")
	head := exec.CommandContext(ctx, "git", "-C", baselineDir, "rev-parse", "HEAD")
	head.Env = env
	out, err := gitCommandCombinedOutput(head)
	if err != nil || strings.TrimSpace(string(out)) != baseline.SHA {
		return fmt.Errorf("working-tree baseline: fetched checkout does not contain %s: %v", baseline.SHA, err)
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", checkoutDir}, args...)...)
		cmd.Env = env
		if out, runErr := gitCommandCombinedOutput(cmd); runErr != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), runErr, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("fetch", "--depth", "1", "--no-tags", "--", baselineDir, baseline.SHA); err != nil {
		return err
	}
	if err := run("update-ref", "refs/remotes/"+baseline.Ref, baseline.SHA); err != nil {
		return err
	}
	// safety: the snapshot commit is parentless, so the baseline reaches HEAD only as a graft.
	if err := run("replace", "--graft", snapshotSHA, baseline.SHA); err != nil {
		return err
	}
	// safety: a runner whose global config turns replacement off would read the graft as absent.
	if err := run("config", "core.useReplaceRefs", "true"); err != nil {
		return err
	}
	return completeShallowCommit(ctx, checkoutDir, snapshotSHA)
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
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read shallow boundary: %w", err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a path under the checkout this call just made
	if err != nil {
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
	return replaceFileAtomically(path, []byte(strings.Join(kept, "\n")+"\n"), info.Mode().Perm())
}

// safety: git reads the boundary at any moment, so it never sees a half-written file.
func replaceFileAtomically(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".shallow-*")
	if err != nil {
		return fmt.Errorf("stage shallow boundary: %w", err)
	}
	name := temp.Name()
	_, writeErr := temp.Write(data)
	closeErr := temp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(fmt.Errorf("write shallow boundary: %w", err), os.Remove(name))
	}
	if err := os.Chmod(name, mode); err != nil {
		return errors.Join(fmt.Errorf("set shallow boundary mode: %w", err), os.Remove(name))
	}
	if err := os.Rename(name, path); err != nil {
		return errors.Join(fmt.Errorf("replace shallow boundary: %w", err), os.Remove(name))
	}
	return nil
}
