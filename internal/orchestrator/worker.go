package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type WorkerOptions struct {
	ControllerURL string

	LogsURL string

	LogStore storage.LogStore

	ArtifactStore storage.ArtifactStore

	HTTPClient *http.Client

	Paths Paths

	PollInterval time.Duration

	HeartbeatInterval time.Duration

	Logger *slog.Logger

	Delegate sparkwing.Logger

	RunnerFactory func(backends Backends, trigger *store.Trigger) runner.Runner

	Token string

	Sources []string
}

func ExecuteClaimedTrigger(ctx context.Context, opts WorkerOptions, backends Backends, stateClient *client.Client, trigger *store.Trigger) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	defer func() {
		if ferr := stateClient.FinishTrigger(ctx, trigger.ID); ferr != nil {
			logger.Warn("finish trigger failed",
				"trigger_id", trigger.ID, "err", ferr)
		}
	}()

	runCtx, cancelRun := context.WithCancel(ctx)
	cancelled := &atomic.Bool{}
	go runHeartbeat(runCtx, stateClient, trigger.ID,
		opts.HeartbeatInterval, cancelRun, cancelled, logger)

	var r runner.Runner
	if opts.RunnerFactory != nil {
		r = opts.RunnerFactory(backends, trigger)
	}
	args := resolveTriggerArgs(runCtx, backends.State, trigger, logger)
	runOpts := Options{
		Pipeline:          trigger.Pipeline,
		RunID:             trigger.ID,
		Args:              args,
		ParentRunID:       trigger.ParentRunID,
		RetryOf:           trigger.RetryOf,
		RetrySource:       trigger.RetrySource,
		RetryRepoDir:      trigger.TriggerEnv[retryprovenance.RepoDirKey],
		RetryRepoIdentity: trigger.TriggerEnv[retryprovenance.RepoIdentityKey],
		RetryRevision:     trigger.TriggerEnv[retryprovenance.RevisionKey],
		RetryPlanHash:     trigger.TriggerEnv[retryprovenance.PlanHashKey],
		Trigger: sparkwing.TriggerInfo{
			Source:      trigger.TriggerSource,
			User:        trigger.TriggerUser,
			PullRequest: sparkwing.PullRequestFromEnv(trigger.TriggerEnv),
		},
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			trigger.GitSHA, trigger.GitBranch, "", trigger.Repo, trigger.RepoURL),
		Delegate: opts.Delegate,
		Runner:   r,
	}

	applyCheckoutProjectConfig(&runOpts, logger)
	res, err := Run(runCtx, backends, runOpts)
	cancelRun()
	if err != nil {
		logger.Error(
			"run failed setup",
			"run_id", trigger.ID,
			"err", err,
		)
		return
	}

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

	logger.Info(
		"run finished",
		"run_id", res.RunID,
		"pipeline", trigger.Pipeline,
		"status", finalStatus,
	)
}

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
	defer func() { _ = dummyStore.Close() }()
	local := LocalBackends(paths, dummyStore, nil)

	stateClient := client.NewWithToken(opts.ControllerURL, opts.HTTPClient, opts.Token)

	logsBackend := local.Logs
	switch {
	case opts.LogStore != nil:
		logsBackend = NewLogStoreBackend(opts.LogStore, opts.Logger)
	case opts.LogsURL != "":
		logsBackend = NewHTTPLogsWithToken(opts.LogsURL, opts.HTTPClient, opts.Token, opts.Logger)
	}
	backends := RemoteBackends(stateClient, logsBackend, opts.ArtifactStore, opts.HTTPClient, store.DefaultConcurrencyLease)

	trigger, err := stateClient.GetTrigger(ctx, triggerID)
	if err != nil {
		return fmt.Errorf("get trigger %s: %w", triggerID, err)
	}
	ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: trigger.ClaimSeq})
	opts.Logger.Info(
		"handling claimed trigger",
		"run_id", trigger.ID,
		"pipeline", trigger.Pipeline,
		"source", trigger.TriggerSource,
	)
	ExecuteClaimedTrigger(ctx, opts, backends, stateClient, trigger)
	return nil
}

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

var (
	runHeartbeatDefaultInterval = 3 * time.Second

	runHeartbeatTimeout = 2 * time.Second

	runHeartbeatMaxSilence = 3 * time.Minute
)
