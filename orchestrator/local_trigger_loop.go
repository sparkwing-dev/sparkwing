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

	"github.com/sparkwing-dev/sparkwing/bincache"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/repos"
)

// runLocalTriggerLoop polls for pending child triggers and dispatches
// each. Compile cache is shared across triggers in the loop lifetime.
func runLocalTriggerLoop(ctx context.Context, st *store.Store, runID string, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
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
				continue
			}
			logger.Warn("local trigger loop: claim failed",
				"parent_run_id", runID, "err", err)
			continue
		}
		if trig == nil {
			continue
		}

		wg.Add(1)
		go func(t *store.Trigger) {
			defer wg.Done()
			if err := dispatchLocalTrigger(ctx, st, t, cache, logger); err != nil {
				logger.Error("local trigger dispatch failed",
					"trigger_id", t.ID, "pipeline", t.Pipeline, "err", err)
				// Mark run failed so the parent's awaiter stops polling.
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
		// ErrNotFound = race lost; try next.
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
// child handles FinishTrigger/FinishRun.
func dispatchLocalTrigger(ctx context.Context, st *store.Store, trig *store.Trigger,
	cache *localCompileCache, logger *slog.Logger) error {

	// Repo resolution: registry by pipeline name first, then slug
	// fallback via LocalRepoDir.
	var repoDir string
	if path, err := repos.ResolveRepoForPipeline(trig.Pipeline); err == nil {
		repoDir = path
	} else if trig.Repo != "" {
		path, lerr := LocalRepoDir(trig.Repo)
		if lerr != nil {
			return fmt.Errorf("locate %q: registry miss + slug fallback failed: registry=%v slug=%w",
				trig.Pipeline, err, lerr)
		}
		repoDir = path
	} else {
		return fmt.Errorf("locate %q: not in repos registry and trigger carries no repo slug "+
			"(register the repo with `sparkwing pipeline add <path>` or pass WithFreshRepo at the call site)",
			trig.Pipeline)
	}
	sparkwingDir := filepath.Join(repoDir, ".sparkwing")
	if _, err := os.Stat(sparkwingDir); err != nil {
		return fmt.Errorf("no .sparkwing/ at %s: %w", sparkwingDir, err)
	}

	binPath, err := cache.compile(sparkwingDir)
	if err != nil {
		return fmt.Errorf("compile %s: %w", sparkwingDir, err)
	}

	logger.Info("local trigger: dispatching child",
		"trigger_id", trig.ID,
		"pipeline", trig.Pipeline,
		"repo", trig.Repo,
		"repo_dir", repoDir,
	)

	// --local MUST precede the positional trigger ID -- Go's flag
	// package stops parsing at the first non-flag, so the reverse
	// order silently falls back to cluster mode.
	cmd := exec.CommandContext(ctx, binPath, "handle-trigger", "--local", trig.ID)
	// cwd drives the SDK's walk-up to .sparkwing/; do NOT pass
	// SPARKWING_WORK_DIR -- it leaks parent-repo paths into children.
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("child exec: %w", err)
	}
	return nil
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
