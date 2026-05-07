// Local-mode child entry point for laptop cross-repo dispatch.
// LocalBackends against the parent's SQLite; no HTTP, no heartbeat.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// HandleClaimedTriggerLocal runs an already-claimed trigger to
// terminal against LocalBackends. Trigger MUST already be 'claimed';
// re-claiming would race the parent's lease bookkeeping.
func HandleClaimedTriggerLocal(ctx context.Context, triggerID string) error {
	logger := slog.Default()

	paths, err := DefaultPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure sparkwing root: %w", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("open local store: %w", err)
	}
	defer st.Close()

	trigger, err := st.GetTrigger(ctx, triggerID)
	if err != nil {
		return fmt.Errorf("get trigger %s: %w", triggerID, err)
	}
	logger.Info("handling claimed trigger (local)",
		"run_id", trigger.ID,
		"pipeline", trigger.Pipeline,
		"parent_run_id", trigger.ParentRunID,
	)

	backends := LocalBackends(paths, st)

	// Parent owns the trigger lifetime; SIGINT cascades via the
	// process group, so no heartbeat or cancel-poll here.
	defer func() {
		if ferr := st.FinishTrigger(ctx, trigger.ID); ferr != nil {
			logger.Warn("finish trigger (local) failed",
				"trigger_id", trigger.ID, "err", ferr)
		}
	}()

	cancelled := &atomic.Bool{}
	_ = cancelled // parity with ExecuteClaimedTrigger; no local cancel path yet.

	var r runner.Runner
	args := resolveTriggerArgs(ctx, backends.State, trigger, logger)
	res, err := Run(ctx, backends, Options{
		Pipeline:    trigger.Pipeline,
		RunID:       trigger.ID,
		Args:        args,
		ParentRunID: trigger.ParentRunID,
		RetryOf:     trigger.RetryOf,
		RetrySource: trigger.RetrySource,
		Trigger: sparkwing.TriggerInfo{
			Source: trigger.TriggerSource,
			User:   trigger.TriggerUser,
			Env:    trigger.TriggerEnv,
		},
		Git:    sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir, trigger.GitSHA, trigger.GitBranch, trigger.Repo, trigger.RepoURL),
		Runner: r,
	})
	if err != nil {
		logger.Error("run failed setup",
			"run_id", trigger.ID,
			"err", err,
		)
		return err
	}
	logger.Info("run finished (local)",
		"run_id", res.RunID,
		"pipeline", trigger.Pipeline,
		"status", res.Status,
	)
	return nil
}
