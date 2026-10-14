package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type WorkerOptions struct {
	ControllerURL string

	LogsURL string

	HeartbeatInterval time.Duration

	Logger *slog.Logger

	Delegate sparkwing.Logger

	RunnerFactory func(backends Backends, trigger *store.Trigger) runner.Runner

	Token string
}

const pipelineSetupFailed = "pipeline setup failed before dispatch; retry the run, check Fleet or local agent status, and give this run ID to an operator if it repeats"

func recordClaimedTriggerSetupFailure(ctx context.Context, state StateBackend, trigger *store.Trigger, cause error) error {
	reason := cause.Error()
	_, defined := sparkwing.Lookup(trigger.Pipeline)
	var configErr *projectConfigError
	if defined && !errors.As(cause, &configErr) {
		reason = pipelineSetupFailed
	}
	run, err := state.GetRun(ctx, trigger.ID)
	if errors.Is(err, store.ErrNotFound) {
		if err := state.CreateRun(ctx, store.Run{
			ID: trigger.ID, Pipeline: trigger.Pipeline, Status: "running",
			TriggerSource: trigger.TriggerSource, GitBranch: trigger.GitBranch, GitSHA: trigger.GitSHA,
			RepoURL:   trigger.RepoURL,
			StartedAt: time.Now(), ParentRunID: trigger.ParentRunID,
		}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if run.Status != "pending" && run.Status != "running" {
		return nil
	}
	if err := state.FinishRun(ctx, trigger.ID, "failed", reason); err != nil {
		if ConfirmTerminalRunAfterWriteError(ctx, state, trigger.ID) {
			return nil
		}
		return err
	}
	return nil
}

// ConfirmTerminalRunAfterWriteError checks whether a failed finish response
// followed a committed terminal write. Its read is bounded independently of
// the write's context, which may have expired before its response arrived.
func ConfirmTerminalRunAfterWriteError(ctx context.Context, state interface {
	GetRun(context.Context, string) (*store.Run, error)
}, runID string,
) bool {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	run, err := state.GetRun(checkCtx, runID)
	return err == nil && run != nil &&
		(run.Status == "success" || run.Status == "failed" || run.Status == "cancelled")
}

func runHeartbeat(ctx context.Context, c *client.Client, triggerID string,
	interval time.Duration,
	cancelRun context.CancelFunc, cancelled *atomic.Bool, logger *slog.Logger,
) {
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastOK := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hbCtx, cancel := context.WithTimeout(ctx, runHeartbeatTimeout)
			status, err := c.HeartbeatTrigger(hbCtx, triggerID)
			cancel()
			if err != nil {
				if client.IsTokenDead(err) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLockHeld) {
					logger.Warn("heartbeat: trigger claim ended; cancelling run",
						"trigger_id", triggerID, "err", err)
					cancelRun()
					return
				}
				silence := time.Since(lastOK)
				if silence >= runHeartbeatMaxSilence {
					logger.Error("heartbeat: controller unreachable beyond lease window; cancelling run",
						"trigger_id", triggerID,
						"silence", silence.Round(time.Second),
						"err", err)
					cancelRun()
					return
				}
				logger.Warn("heartbeat failed",
					"trigger_id", triggerID,
					"err", err,
					"silence", silence.Round(time.Second))
				continue
			}
			lastOK = time.Now()
			if status != nil && status.CancelRequested {
				if cancelled.CompareAndSwap(false, true) {
					logger.Info("operator cancel requested; cancelling run ctx",
						"trigger_id", triggerID)
					cancelRun()
				}
			}
		}
	}
}

var (
	runHeartbeatTimeout = 2 * time.Second

	runHeartbeatMaxSilence = 3 * time.Minute
)
