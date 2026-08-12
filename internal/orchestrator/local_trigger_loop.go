// Laptop equivalent of cluster's trigger loop. Claims pending
// triggers parented to runID and dispatches each by compiling the
// target repo's .sparkwing/ and exec'ing handle-trigger --local.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	sparkwinggit "github.com/sparkwing-dev/sparkwing/sparkwing/git"
)

// runLocalTriggerLoop polls for pending child triggers and dispatches
// each. Compile cache is shared across triggers in the loop lifetime.
// profileName, when non-empty, is forwarded to each child as
// --profile <name> so the child opens the same backends as the parent
// -- critical when the parent is on postgres or another non-local
// state, since the child handler defaults to sqlite otherwise. The
// caller resolves (and error-checks) wedgeBudget before spawning the
// loop.
// parentRepoDir, when non-empty, is the directory of the running
// parent's .sparkwing/ tree. A same-repo child (RunAndAwait to a
// sibling pipeline) is dispatched straight from that already-compiled
// binary, so the dispatch needs no repo registry entry and no git
// identity on the project directory.
func runLocalTriggerLoop(ctx context.Context, st *store.Store, runID, profileName, parentRepoDir string, logger *slog.Logger, wedgeBudget time.Duration) {
	if logger == nil {
		logger = slog.Default()
	}
	wedge := newStoreWedgeGuard(wedgeBudget)
	cache := &localCompileCache{}
	defer cache.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		trig, err := claimChildTrigger(ctx, st, runID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				wedge.success()
				continue
			}
			if terminal := wedge.fail("local trigger loop: claim trigger", err); terminal != nil {
				logger.Error("local trigger loop stopping; store wedged",
					"parent_run_id", runID, "err", terminal)
				return
			}
			logger.Warn("local trigger loop: claim failed",
				"parent_run_id", runID, "err", err)
			continue
		}
		wedge.success()
		if trig == nil {
			continue
		}

		wg.Add(1)
		go func(t *store.Trigger) {
			defer wg.Done()
			if err := dispatchLocalTrigger(ctx, st, t, profileName, parentRepoDir, cache, logger); err != nil {
				logger.Error("local trigger dispatch failed",
					"trigger_id", t.ID, "pipeline", t.Pipeline, "err", err)
				_ = st.CreateRun(ctx, store.Run{
					ID:        t.ID,
					Pipeline:  t.Pipeline,
					Status:    "failed",
					StartedAt: time.Now(),
				})
				_ = st.FinishRun(ctx, t.ID, "failed", "local dispatch: "+err.Error())
				_ = st.FinishTrigger(ctx, t.ID)
			}
		}(trig)
	}
}

// claimChildTrigger claims the oldest pending trigger parented to
// runID. Filtering keeps multi-run sessions from stealing each
// other's children.
func claimChildTrigger(ctx context.Context, st *store.Store, runID string) (*store.Trigger, error) {
	candidates, err := st.ListPendingTriggersForParent(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, id := range candidates {
		t, err := st.ClaimSpecificTrigger(ctx, id, store.DefaultLeaseDuration)
		if err == nil {
			return t, nil
		}
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		return nil, err
	}
	return nil, store.ErrNotFound
}

// dispatchLocalTrigger compiles and execs a claimed trigger. The
// child handles FinishTrigger/FinishRun. profileName, when non-empty,
// is forwarded as --profile <name> so the child opens the same
// backends as the parent (matters for postgres/non-local state).
func dispatchLocalTrigger(ctx context.Context, st *store.Store, trig *store.Trigger,
	profileName, parentRepoDir string, cache *localCompileCache, logger *slog.Logger,
) error {
	repoDir, cleanup, err := prepareTriggerRepo(ctx, trig, parentRepoDir)
	if err != nil {
		return err
	}
	defer cleanup()
	sparkwingDir := filepath.Join(repoDir, ".sparkwing")
	if _, err := os.Stat(sparkwingDir); err != nil {
		return fmt.Errorf("no .sparkwing/ at %s: %w", sparkwingDir, err)
	}

	binPath, err := cache.compile(sparkwingDir)
	if err != nil {
		return fmt.Errorf("compile %s: %w", sparkwingDir, err)
	}

	logger.Info(
		"local trigger: dispatching child",
		"trigger_id", trig.ID,
		"pipeline", trig.Pipeline,
		"repo", trig.Repo,
		"repo_dir", repoDir,
	)

	args := []string{"handle-trigger", "--local"}
	if profileName != "" {
		args = append(args, "--profile", profileName)
	}
	args = append(args, trig.ID)
	return execLocalChild(ctx, binPath, repoDir, args)
}

func execLocalChild(ctx context.Context, binPath, repoDir string, args []string) error {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(binPath); os.IsNotExist(statErr) {
				return fmt.Errorf(
					"child executable lease %q is unavailable (local pipeline-cache provenance). "+
						"Re-run the parent pipeline to recreate its executable lease; if the shared cache is corrupt, "+
						"run `sparkwing pipeline sparks warmup --clear-cache`: %w",
					binPath, err,
				)
			}
		}
		return fmt.Errorf("child exec: %w", err)
	}
	return nil
}

// prepareTriggerRepo returns the tree whose files may be compiled and executed.
// Ordinary triggers use their located checkout directly. Retries instead use a
// detached temporary worktree materialized at the source run's recorded commit.
// This makes the recorded revision the content boundary: uncommitted changes in
// the original checkout, including changes made after validation, cannot affect
// the retry even when they preserve the pipeline plan's shape.
func prepareTriggerRepo(ctx context.Context, trig *store.Trigger, parentRepoDir string) (string, func(), error) {
	repoDir, err := locateTriggerRepo(ctx, trig, parentRepoDir)
	if err != nil {
		return "", func() {}, err
	}
	if trig.RetryOf == "" {
		return repoDir, func() {}, nil
	}

	revision := strings.TrimSpace(trig.TriggerEnv[retryprovenance.RevisionKey])
	tempRoot, err := os.MkdirTemp("", "sparkwing-retry-")
	if err != nil {
		return "", func() {}, &RetrySourceUnavailableError{
			RepoDir: repoDir,
			Reason:  "create recorded-revision snapshot: " + err.Error(),
		}
	}
	snapshotDir := filepath.Join(tempRoot, "checkout")
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "worktree", "add", "--detach", snapshotDir, revision)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(tempRoot)
		return "", func() {}, &RetrySourceUnavailableError{
			RepoDir: repoDir,
			Reason:  fmt.Sprintf("materialize recorded revision %q: %v: %s", revision, err, strings.TrimSpace(string(out))),
		}
	}

	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, "git", "-C", repoDir, "worktree", "remove", "--force", snapshotDir).Run()
		_ = os.RemoveAll(tempRoot)
	}
	actualRevision, err := sparkwinggit.CurrentSHA(ctx, snapshotDir)
	if err != nil || !strings.EqualFold(actualRevision, revision) {
		cleanup()
		reason := fmt.Sprintf("recorded-revision snapshot is %q, want %q", actualRevision, revision)
		if err != nil {
			reason = "verify recorded-revision snapshot: " + err.Error()
		}
		return "", func() {}, &RetrySourceUnavailableError{RepoDir: repoDir, Reason: reason}
	}
	return snapshotDir, cleanup, nil
}

// locateTriggerRepo maps a claimed trigger to the repo directory whose
// .sparkwing/ defines it. A same-repo child (no explicit repo slug)
// resolves against the running parent's own tree first, so it needs
// neither a registry entry nor a git identity on the project directory.
// Only when that fast path does not apply does it consult the cross-repo
// registry and the "owner/name" slug fallback.
func locateTriggerRepo(ctx context.Context, trig *store.Trigger, parentRepoDir string) (string, error) {
	if trig.RetryOf != "" {
		return locateRetryRepo(ctx, trig)
	}
	if dir, err := submittedTriggerRepoDir(trig); dir != "" || err != nil {
		return dir, err
	}
	if parentRepoDir != "" && triggerUsesParentRepo(trig) && repoDeclaresPipeline(parentRepoDir, trig.Pipeline) {
		return parentRepoDir, nil
	}
	path, err := repos.ResolveRepoForPipelineCached(trig.Pipeline)
	if err == nil {
		return path, nil
	}
	if trig.Repo != "" {
		slugPath, lerr := LocalRepoDir(trig.Repo)
		if lerr != nil {
			return "", fmt.Errorf("locate %q: registry miss + slug fallback failed: registry=%w slug=%w",
				trig.Pipeline, err, lerr)
		}
		return slugPath, nil
	}
	return "", unlocatableChildError(trig.Pipeline)
}

func triggerUsesParentRepo(trig *store.Trigger) bool {
	return trig.Repo == "" || trig.RepoInherited
}

// SubmitRepoDirKey is the TriggerEnv entry `sparkwing runs submit`
// writes to name the checkout whose .sparkwing/ defines the submitted
// pipeline. It is private metadata in the same sense as the
// retryprovenance keys: persisted on the trigger, never exported into a
// pipeline process environment.
//
// Submission needs it because the submitter and the executor are
// different processes. The registry resolves a pipeline name to
// whichever registered repo declares it, which is the right answer for a
// spawned child and the wrong one for a person standing in a checkout
// that is not registered -- or, worse, is one of two checkouts of the
// same project, where the registry would silently pick the other. The
// submitter knows which tree it was standing in; recording it is how
// that survives the handoff.
const SubmitRepoDirKey = "_SPARKWING_SUBMIT_REPO_DIR"

// submittedTriggerRepoDir resolves the checkout a submitted trigger
// named, or ("", nil) when the trigger carries no submission repo (every
// trigger from the webhook, spawn, and retry paths).
//
// A recorded directory that no longer holds a .sparkwing/ is an error,
// not a reason to fall back: falling back to the registry would run a
// different copy of the pipeline than the submitter chose, and doing
// that silently is exactly the confusion recording the path prevents.
func submittedTriggerRepoDir(trig *store.Trigger) (string, error) {
	raw := strings.TrimSpace(trig.TriggerEnv[SubmitRepoDirKey])
	if raw == "" {
		return "", nil
	}
	dir := filepath.Clean(raw)
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("submitted trigger %s: recorded repo dir %q is not absolute", trig.ID, raw)
	}
	if info, err := os.Stat(filepath.Join(dir, ".sparkwing")); err != nil || !info.IsDir() {
		return "", fmt.Errorf(
			"submitted trigger %s: the checkout it was submitted from no longer has a .sparkwing/ at %s; "+
				"resubmit from the checkout that defines %q", trig.ID, dir, trig.Pipeline)
	}
	return dir, nil
}

// RetrySourceUnavailableError is returned when a local retry cannot prove that
// it is about to execute in the source attempt's exact checkout. Retrying from
// an ambient cwd, a same-named checkout, or the repo registry is intentionally
// forbidden: all three can silently execute a different pipeline definition.
type RetrySourceUnavailableError struct {
	RepoDir string
	Reason  string
}

func (e *RetrySourceUnavailableError) Error() string {
	if e.RepoDir == "" {
		return "retry source worktree unavailable: " + e.Reason
	}
	return fmt.Sprintf("retry source worktree unavailable at %s: %s", e.RepoDir, e.Reason)
}

func locateRetryRepo(ctx context.Context, trig *store.Trigger) (string, error) {
	if trig.TriggerEnv[retryprovenance.PlanHashKey] == "" {
		return "", &RetrySourceUnavailableError{Reason: "source run did not record a plan identity"}
	}
	expectedIdentity := strings.TrimSpace(trig.TriggerEnv[retryprovenance.RepoIdentityKey])
	if expectedIdentity == "" {
		return "", &RetrySourceUnavailableError{Reason: "source run did not record a full repository identity"}
	}
	expectedRevision := strings.TrimSpace(trig.TriggerEnv[retryprovenance.RevisionKey])
	if expectedRevision == "" {
		return "", &RetrySourceUnavailableError{Reason: "source run did not record a Git revision"}
	}
	repoDir := filepath.Clean(trig.TriggerEnv[retryprovenance.RepoDirKey])
	if repoDir == "." || repoDir == "" {
		return "", &RetrySourceUnavailableError{Reason: "source run did not record a repository root"}
	}
	if !filepath.IsAbs(repoDir) {
		return "", &RetrySourceUnavailableError{RepoDir: repoDir, Reason: "recorded path is not absolute"}
	}
	resolved, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return "", &RetrySourceUnavailableError{RepoDir: repoDir, Reason: err.Error()}
	}
	if err := assertGitDir(resolved); err != nil {
		return "", &RetrySourceUnavailableError{RepoDir: resolved, Reason: "not a git checkout: " + err.Error()}
	}
	if info, err := os.Stat(filepath.Join(resolved, ".sparkwing")); err != nil || !info.IsDir() {
		reason := "missing .sparkwing directory"
		if err != nil {
			reason += ": " + err.Error()
		}
		return "", &RetrySourceUnavailableError{RepoDir: resolved, Reason: reason}
	}
	actualIdentity, err := sparkwinggit.RemoteOriginURL(ctx, resolved)
	if err != nil {
		return "", &RetrySourceUnavailableError{RepoDir: resolved, Reason: "read repository identity: " + err.Error()}
	}
	if actualIdentity != expectedIdentity {
		return "", &RetrySourceUnavailableError{
			RepoDir: resolved,
			Reason:  fmt.Sprintf("repository identity drift: recorded %q, checkout is %q", expectedIdentity, actualIdentity),
		}
	}
	actualRevision, err := sparkwinggit.CurrentSHA(ctx, resolved)
	if err != nil {
		return "", &RetrySourceUnavailableError{RepoDir: resolved, Reason: "read checkout revision: " + err.Error()}
	}
	if !strings.EqualFold(actualRevision, expectedRevision) {
		return "", &RetrySourceUnavailableError{
			RepoDir: resolved,
			Reason:  fmt.Sprintf("checkout revision drift: recorded %q, checkout is %q", expectedRevision, actualRevision),
		}
	}
	return resolved, nil
}

// unlocatableChildError describes a same-repo child that resolved
// nowhere: it names the real cause (no git identity to inherit a repo
// slug from) and three concrete fixes, and deliberately never mentions a
// verb the CLI does not have.
func unlocatableChildError(pipeline string) error {
	return fmt.Errorf("locate %q: not declared by the running project, absent from the repo "+
		"registry, and this run has no git identity to resolve a sibling checkout from. Give the "+
		"project a git remote, register the defining repo with `sparkwing configure xrepo add <path>`, "+
		"or pass sparkwing.WithFreshRepo(\"owner/name\") for a cross-repo await.",
		pipeline)
}

// repoDeclaresPipeline reports whether repoDir's compiled .sparkwing/
// binary registers a pipeline by this name. The parent's binary is
// already in the compile cache, so the same-repo check is a cache hit
// rather than a fresh build even under host contention.
func repoDeclaresPipeline(repoDir, pipeline string) bool {
	names, err := repos.PipelineNamesForRepo(repoDir)
	if err != nil {
		return false
	}
	for _, n := range names {
		if n == pipeline {
			return true
		}
	}
	return false
}

// localCompileCache owns executable leases for one trigger-consumer lifetime.
// A path under the shared binary cache is only a source: another process may
// clear or replace that cache while a parent run is still alive. Before a path
// reaches exec, the cache hard-links (or copies) it into a private temporary
// directory that is removed only after every dispatched child has exited.
type localCompileCache struct {
	mu       sync.Mutex
	hit      map[string]string
	bySource map[string]string
	leaseDir string
}

func (c *localCompileCache) compile(sparkwingDir string) (string, error) {
	source := filepath.Clean(sparkwingDir)
	c.mu.Lock()
	if hash := c.bySource[source]; hash != "" {
		if p := c.hit[hash]; p != "" {
			if _, err := os.Stat(p); err == nil {
				c.mu.Unlock()
				return p, nil
			}
			delete(c.hit, hash)
		}
		delete(c.bySource, source)
	}
	c.mu.Unlock()

	hash, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", sparkwingDir, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.hit[hash]; p != "" {
		if _, err := os.Stat(p); err == nil {
			if c.bySource == nil {
				c.bySource = map[string]string{}
			}
			c.bySource[source] = hash
			return p, nil
		}
		delete(c.hit, hash)
	}

	binPath := bincache.CachedBinaryPath(hash)
	var leaseErr error
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := os.Stat(binPath); err != nil {
			if !os.IsNotExist(err) {
				return "", fmt.Errorf("stat binary cache: %w", err)
			}
			if err := bincache.CompilePipeline(sparkwingDir, binPath); err != nil {
				return "", err
			}
		}

		if c.leaseDir == "" {
			c.leaseDir, err = os.MkdirTemp("", "sparkwing-child-executables-")
			if err != nil {
				return "", fmt.Errorf("create child executable lease: %w", err)
			}
		}
		leasedPath := filepath.Join(c.leaseDir, hash+"-"+filepath.Base(binPath))
		leaseErr = leaseExecutable(binPath, leasedPath)
		if leaseErr == nil {
			if c.hit == nil {
				c.hit = map[string]string{}
			}
			if c.bySource == nil {
				c.bySource = map[string]string{}
			}
			c.hit[hash] = leasedPath
			c.bySource[source] = hash
			return leasedPath, nil
		}
		if !os.IsNotExist(leaseErr) {
			return "", fmt.Errorf("lease cached pipeline executable %s: %w", binPath, leaseErr)
		}
	}

	return "", fmt.Errorf(
		"lease cached pipeline executable %s: cache entry disappeared while acquiring its lifetime lease; "+
			"re-run the parent pipeline (or rebuild a corrupt cache with `sparkwing pipeline sparks warmup --clear-cache`): %w",
		binPath, leaseErr,
	)
}

// Close releases all executables after the trigger loop has waited for its
// in-flight children. It is safe on a zero-value or already-closed cache.
func (c *localCompileCache) Close() error {
	c.mu.Lock()
	dir := c.leaseDir
	c.leaseDir = ""
	c.hit = nil
	c.bySource = nil
	c.mu.Unlock()
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}

// leaseExecutable gives the consumer a pathname whose lifetime is independent
// of the shared cache entry. A hard link is cheap and preserves the inode even
// if the cache is cleared. Filesystems that cannot link across the temporary
// boundary fall back to copying through an open descriptor, which remains
// readable if the source name is concurrently unlinked.
func leaseExecutable(src, dest string) error {
	if err := os.Link(src, dest); err == nil {
		return nil
	} else if os.IsExist(err) {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".lease-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		_ = tmp.Close()
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	removeTmp = false
	return nil
}
