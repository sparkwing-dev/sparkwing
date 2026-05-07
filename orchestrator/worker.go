package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WorkerOptions configures a worker's polling loop.
type WorkerOptions struct {
	// ControllerURL is the controller base URL.
	ControllerURL string

	// LogsURL, when non-empty, routes per-node log lines to a
	// sparkwing-logs service. Ignored if LogStore is set.
	LogsURL string

	// LogStore, when non-nil, takes precedence over LogsURL.
	LogStore storage.LogStore

	// HTTPClient transport for controller calls. Nil = default 30s.
	HTTPClient *http.Client

	// Paths resolves on-disk locations for locks and log files. Zero
	// value uses DefaultPaths.
	Paths Paths

	// PollInterval is the wait between empty-queue polls. Zero = 1s.
	PollInterval time.Duration

	// HeartbeatInterval is the cadence of claim-lease heartbeats.
	// Zero uses store.DefaultLeaseDuration / 3.
	HeartbeatInterval time.Duration

	// Logger receives lifecycle events. Nil uses slog.Default.
	Logger *slog.Logger

	// Delegate, when non-nil, mirrors every node log line.
	Delegate sparkwing.Logger

	// RunnerFactory returns the Runner each claimed trigger should
	// use; called once per trigger so the factory can close over the
	// claim. Nil means default InProcessRunner.
	RunnerFactory func(backends Backends, trigger *store.Trigger) runner.Runner

	// Token is the shared-secret bearer for controller + logs calls.
	// Empty = no auth header.
	Token string

	// Sources filters trigger_source values this worker accepts.
	// Empty/nil = accept any source.
	Sources []string
}

// ExecuteClaimedTrigger runs a single trigger to terminal state.
// Always flips the trigger to 'done' before returning, even on setup
// failure -- otherwise the reaper would re-queue and the next worker
// would hit the same error, infinite-looping.
func ExecuteClaimedTrigger(ctx context.Context, opts WorkerOptions, backends Backends, stateClient *client.Client, trigger *store.Trigger) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// Uses ctx (not runCtx) so a mid-run shutdown still finalizes.
	defer func() {
		if ferr := stateClient.FinishTrigger(ctx, trigger.ID); ferr != nil {
			logger.Warn("finish trigger failed",
				"trigger_id", trigger.ID, "err", ferr)
		}
	}()

	// Heartbeat keeps the claim alive and propagates operator cancel
	// requests via runCtx.
	runCtx, cancelRun := context.WithCancel(ctx)
	cancelled := &atomic.Bool{}
	go runHeartbeat(runCtx, stateClient, trigger.ID,
		opts.HeartbeatInterval, cancelRun, cancelled, logger)

	var r runner.Runner
	if opts.RunnerFactory != nil {
		r = opts.RunnerFactory(backends, trigger)
	}
	args := resolveTriggerArgs(runCtx, backends.State, trigger, logger)
	res, err := Run(runCtx, backends, Options{
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
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			trigger.GitSHA, trigger.GitBranch, trigger.Repo, trigger.RepoURL),
		Delegate: opts.Delegate,
		Runner:   r,
	})
	cancelRun()
	if err != nil {
		logger.Error("run failed setup",
			"run_id", trigger.ID,
			"err", err,
		)
		return
	}

	// On operator cancel, overwrite state and sweep in-flight nodes.
	finalStatus := res.Status
	if cancelled.Load() {
		finalStatus = "cancelled"
		_ = stateClient.FinishRun(ctx, res.RunID, "cancelled", "cancelled by operator")

		nodes, nerr := stateClient.ListNodes(ctx, res.RunID)
		if nerr == nil {
			for _, n := range nodes {
				if n.Status == "done" {
					continue
				}
				_ = stateClient.FinishNode(ctx, res.RunID, n.NodeID,
					string(sparkwing.Cancelled), "cancelled by operator", nil)
			}
		}
	}

	logger.Info("run finished",
		"run_id", res.RunID,
		"pipeline", trigger.Pipeline,
		"status", finalStatus,
	)
}

// HandleClaimedTrigger adopts an already-claimed trigger and runs it
// to terminal state. Caller's lease is still live; this function's
// heartbeat extends it.
func HandleClaimedTrigger(ctx context.Context, opts WorkerOptions, triggerID string) error {
	if opts.ControllerURL == "" {
		return errors.New("WorkerOptions.ControllerURL is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	paths := opts.Paths
	if paths.Root == "" {
		p, err := DefaultPaths()
		if err != nil {
			return fmt.Errorf("resolve paths: %w", err)
		}
		paths = p
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure sparkwing root: %w", err)
	}

	dummyStore, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("open local store: %w", err)
	}
	defer dummyStore.Close()
	local := LocalBackends(paths, dummyStore)

	stateClient := client.NewWithToken(opts.ControllerURL, opts.HTTPClient, opts.Token)

	var logsBackend LogBackend = local.Logs
	switch {
	case opts.LogStore != nil:
		logsBackend = NewLogStoreBackend(opts.LogStore, opts.Logger)
	case opts.LogsURL != "":
		logsBackend = NewHTTPLogsWithToken(opts.LogsURL, opts.HTTPClient, opts.Token, opts.Logger)
	}
	// Concurrency must go through the controller so cache hits, slot
	// holders, and waiter resolution are shared across runner pods.
	backends := Backends{
		State:       stateClient,
		Logs:        logsBackend,
		Concurrency: NewHTTPConcurrency(opts.ControllerURL, opts.HTTPClient, opts.Token, store.DefaultConcurrencyLease),
	}
	_ = local // local backends still useful for paths/logs fallback

	trigger, err := stateClient.GetTrigger(ctx, triggerID)
	if err != nil {
		return fmt.Errorf("get trigger %s: %w", triggerID, err)
	}
	opts.Logger.Info("handling claimed trigger",
		"run_id", trigger.ID,
		"pipeline", trigger.Pipeline,
		"source", trigger.TriggerSource,
	)
	ExecuteClaimedTrigger(ctx, opts, backends, stateClient, trigger)
	return nil
}

// runHeartbeat POSTs heartbeats until ctx is cancelled. On a cancel
// signal from the controller, cancels the run and records the fact in
// `cancelled` so the caller marks 'cancelled' rather than 'failed'.
// ErrNotFound means we lost the claim -- cancel the run ctx so we
// don't write to a dead run. Self-terminates after
// runHeartbeatMaxSilence of consecutive failures.
func runHeartbeat(ctx context.Context, c *client.Client, triggerID string,
	interval time.Duration,
	cancelRun context.CancelFunc, cancelled *atomic.Bool, logger *slog.Logger,
) {
	if interval <= 0 {
		interval = runHeartbeatDefaultInterval
	}
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
			// Short ctx so a wedged controller can't block the ticker.
			hbCtx, cancel := context.WithTimeout(ctx, runHeartbeatTimeout)
			status, err := c.HeartbeatTrigger(hbCtx, triggerID)
			cancel()
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					logger.Warn("heartbeat: trigger reaped; cancelling run",
						"trigger_id", triggerID)
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

// Vars (not consts) so tests can shrink them.
var (
	runHeartbeatDefaultInterval = 3 * time.Second

	// Strictly less than runHeartbeatDefaultInterval so a wedged
	// controller can't stack ticks.
	runHeartbeatTimeout = 2 * time.Second

	// Cancel the run after this much consecutive failure; matches
	// store.DefaultLeaseDuration.
	runHeartbeatMaxSilence = 3 * time.Minute
)
