// Laptop equivalent of cluster's trigger loop. Claims pending
// triggers parented to runID and dispatches each by compiling the
// target repo's .sparkwing/ and exec'ing handle-trigger --local.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

// RunLocalTriggerConsumer polls for pending triggers (including ones
// created by web/CLI retry against the same SQLite store) and
// dispatches each via the compile+exec path. Use this in long-lived
// laptop dev servers (localws) so retried/queued runs actually
// execute instead of sitting in the trigger table.
//
// The compile cache is shared across the consumer's lifetime so
// back-to-back triggers against the same .sparkwing/ skip the rebuild.
// An unparseable [StoreWedgeBudgetEnvVar] is a startup error so the
// misconfiguration fails the caller instead of silently leaving
// queued triggers unconsumed; on success the consumer runs in its own
// goroutine until ctx cancels, letting in-flight dispatches finish
// first.
func RunLocalTriggerConsumer(ctx context.Context, st *store.Store, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	wedge, err := newStoreWedgeGuardFromEnv()
	if err != nil {
		return fmt.Errorf("local trigger consumer: %w", err)
	}
	go consumeLocalTriggers(ctx, st, logger, wedge)
	return nil
}

// consumeLocalTriggers is RunLocalTriggerConsumer's claim/dispatch
// loop, split out so validation happens synchronously at startup.
func consumeLocalTriggers(ctx context.Context, st *store.Store, logger *slog.Logger, wedge *storeWedgeGuard) {
	cache := &localCompileCache{}
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

		trig, err := st.ClaimNextTrigger(ctx, store.DefaultLeaseDuration)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				wedge.success()
				continue
			}
			if terminal := wedge.fail("local trigger consumer: claim trigger", err); terminal != nil {
				logger.Error("local trigger consumer stopping; store wedged", "err", terminal)
				return
			}
			logger.Warn("local trigger consumer: claim failed", "err", err)
			continue
		}
		wedge.success()
		if trig == nil {
			continue
		}

		wg.Add(1)
		go func(t *store.Trigger) {
			defer wg.Done()
			if err := dispatchLocalTrigger(ctx, st, t, "", "", cache, logger); err != nil {
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
	repoDir, err := locateTriggerRepo(trig, parentRepoDir)
	if err != nil {
		return err
	}
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
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("child exec: %w", err)
	}
	return nil
}

// locateTriggerRepo maps a claimed trigger to the repo directory whose
// .sparkwing/ defines it. A same-repo child (no explicit repo slug)
// resolves against the running parent's own tree first, so it needs
// neither a registry entry nor a git identity on the project directory.
// Only when that fast path does not apply does it consult the cross-repo
// registry and the "owner/name" slug fallback.
func locateTriggerRepo(trig *store.Trigger, parentRepoDir string) (string, error) {
	if parentRepoDir != "" && trig.Repo == "" && repoDeclaresPipeline(parentRepoDir, trig.Pipeline) {
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

// localCompileCache memoizes sparkwingDir -> binPath for the loop's
// lifetime so back-to-back awaits skip even the hash compute.
type localCompileCache struct {
	mu  sync.Mutex
	hit map[string]string
}

func (c *localCompileCache) compile(sparkwingDir string) (string, error) {
	c.mu.Lock()
	if c.hit != nil {
		if p, ok := c.hit[sparkwingDir]; ok {
			c.mu.Unlock()
			return p, nil
		}
	}
	c.mu.Unlock()

	hash, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", sparkwingDir, err)
	}
	binPath := bincache.CachedBinaryPath(hash)
	if _, err := os.Stat(binPath); err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat binary cache: %w", err)
		}
		if err := bincache.CompilePipeline(sparkwingDir, binPath); err != nil {
			return "", err
		}
	}

	c.mu.Lock()
	if c.hit == nil {
		c.hit = map[string]string{}
	}
	c.hit[sparkwingDir] = binPath
	c.mu.Unlock()
	return binPath, nil
}
