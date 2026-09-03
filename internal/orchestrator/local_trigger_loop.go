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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	sparkwinggit "github.com/sparkwing-dev/sparkwing/sparkwing/git"
)

var gitObjectRE = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

// childStoreEnv is the store a parent already chose, handed to the children it
// dispatches so they land in it rather than deriving one of their own.
type childStoreEnv struct {
	path   string
	reason string
}

// safety: an empty path is the pre-design default on purpose: the dashboard's
// trigger consumer claims from the shared store, so a child it dispatches must
// open that same file when it cannot be hosted.
func (c childStoreEnv) apply(env []string) []string {
	if c.path == "" {
		return env
	}
	if env == nil {
		env = os.Environ()
	}
	return append(env, StandaloneStateDBEnv+"="+c.path, StandaloneReasonEnv+"="+c.reason)
}

func runLocalTriggerLoop(ctx context.Context, state StateBackend, runID, profileName, parentRepoDir string, logger *slog.Logger, wedgeBudget time.Duration, childStore childStoreEnv) {
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

	firstObservation := true
	for {
		if firstObservation {
			firstObservation = false
			select {
			case <-ctx.Done():
				return
			default:
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}

		trig, err := claimChildTrigger(ctx, state, runID)
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
			if err := dispatchLocalTrigger(ctx, t, profileName, parentRepoDir, cache, logger, childStore.apply(nil)); err != nil {
				logger.Error("local trigger dispatch failed",
					"trigger_id", t.ID, "pipeline", t.Pipeline, "err", err)
				_ = state.CreateRun(ctx, store.Run{
					ID:        t.ID,
					Pipeline:  t.Pipeline,
					Status:    "failed",
					StartedAt: time.Now(),
				})
				_ = state.FinishRun(ctx, t.ID, "failed", "local dispatch: "+err.Error())
				_ = state.FinishTrigger(ctx, t.ID)
			}
		}(trig)
	}
}

func claimChildTrigger(ctx context.Context, state RunCoordination, runID string) (*store.Trigger, error) {
	candidates, err := state.ListPendingTriggersForParent(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, id := range candidates {
		t, err := state.ClaimSpecificTrigger(ctx, id, store.DefaultLeaseDuration)
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

func dispatchLocalTrigger(ctx context.Context, trig *store.Trigger,
	profileName, parentRepoDir string, cache *localCompileCache, logger *slog.Logger, env []string,
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
	return execLocalChild(ctx, binPath, repoDir, args, env)
}

func submissionExecutionEnvironment(captured []string, home string) []string {
	if captured == nil {
		// safety: an uncaptured dispatch still passes the blocklist, so the consumer shell cannot shape the run.
		captured = os.Environ()
	}
	blocked := map[string]struct{}{
		"SPARKWING_RUN_HANDLE_FILE": {},
		"SPARKWING_START_AT":        {}, "SPARKWING_STOP_AT": {}, "SPARKWING_ONLY": {},
		"SPARKWING_NO_CACHE": {}, "SPARKWING_DRY_RUN": {}, "SPARKWING_LOCAL_ONLY": {},
		"SPARKWING_ALLOW": {}, "SPARKWING_REF": {}, "SPARKWING_SECRETS_PROFILE": {},
		"SPARKWING_MODE": {}, "SPARKWING_WORKERS": {}, "SPARKWING_DISPATCH_WAIT_TIMEOUT": {},
		"SPARKWING_DEBUG_PAUSE_BEFORE": {}, "SPARKWING_DEBUG_PAUSE_AFTER": {},
		"SPARKWING_DEBUG_PAUSE_ON_FAILURE": {},
	}
	out := make([]string, 0, len(captured)+1)
	for _, entry := range captured {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, denied := blocked[key]; !denied && key != "SPARKWING_HOME" {
			out = append(out, entry)
		}
	}
	return append(out, "SPARKWING_HOME="+home)
}

func execLocalChild(ctx context.Context, binPath, repoDir string, args, env []string) error {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
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
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "worktree", "add", "--detach", "--", snapshotDir, revision)
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

const SubmitRepoDirKey = "_SPARKWING_SUBMIT_REPO_DIR"

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
	// safety: the recorded revision reaches git as a positional argument, so only an object id may pass.
	if !gitObjectRE.MatchString(expectedRevision) {
		return "", &RetrySourceUnavailableError{
			Reason: fmt.Sprintf("recorded revision %q is not a git object id", expectedRevision),
		}
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

func unlocatableChildError(pipeline string) error {
	return fmt.Errorf("locate %q: not declared by the running project, absent from the repo "+
		"registry, and this run has no git identity to resolve a sibling checkout from. Give the "+
		"project a git remote, register the defining repo with `sparkwing configure xrepo add <path>`, "+
		"or pass sparkwing.WithFreshRepo(\"owner/name\") for a cross-repo await",
		pipeline)
}

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

	entry, err := bincache.PipelineEntry(hash)
	if err != nil {
		return "", fmt.Errorf("cache entry: %w", err)
	}
	lease, _, err := entry.AcquireOrMaterialize(context.Background(), func(tempPath string) error {
		return bincache.CompilePipeline(sparkwingDir, tempPath)
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = lease.Release() }()
	if c.leaseDir == "" {
		c.leaseDir, err = os.MkdirTemp("", "sparkwing-child-executables-")
		if err != nil {
			return "", fmt.Errorf("create child executable lease: %w", err)
		}
	}
	leasedPath := filepath.Join(c.leaseDir, hash+"-"+filepath.Base(lease.Path()))
	if err := leaseExecutable(lease.Path(), leasedPath); err != nil {
		return "", fmt.Errorf("lease cached pipeline executable %s: %w", lease.Path(), err)
	}
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
