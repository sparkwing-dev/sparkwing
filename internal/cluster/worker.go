package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// RunWorker claims and executes triggers from the controller until
// ctx is cancelled. Graceful shutdown: in-flight runs complete, new
// polls stop. Returns nil on clean shutdown; a non-nil error only
// for setup problems that make the worker unable to function
// (unreachable controller at startup is NOT such a problem -- we
// log and keep polling).
func RunWorker(ctx context.Context, opts orchestrator.WorkerOptions) error {
	if opts.ControllerURL == "" {
		return errors.New("WorkerOptions.ControllerURL is required")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 1 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	paths := opts.Paths
	if paths.Root == "" {
		p, err := orchestrator.DefaultPaths()
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
		return fmt.Errorf("open local store for logs fallback: %w", err)
	}
	defer func() { _ = dummyStore.Close() }()
	local := orchestrator.LocalBackends(paths, dummyStore)

	stateClient := client.NewWithToken(opts.ControllerURL, opts.HTTPClient, opts.Token)

	logsBackend := local.Logs
	switch {
	case opts.LogStore != nil:
		logsBackend = orchestrator.NewLogStoreBackend(opts.LogStore, opts.Logger)
	case opts.LogsURL != "":
		logsBackend = orchestrator.NewHTTPLogsWithToken(opts.LogsURL, opts.HTTPClient, opts.Token, opts.Logger)
	}

	backends := orchestrator.Backends{
		State:       stateClient,
		Logs:        logsBackend,
		Concurrency: orchestrator.NewHTTPConcurrency(opts.ControllerURL, opts.HTTPClient, opts.Token, store.DefaultConcurrencyLease),
	}

	knownPipelines := sparkwing.Registered()
	opts.Logger.Info(
		"worker started",
		"controller", opts.ControllerURL,
		"logs", opts.LogsURL,
		"poll", opts.PollInterval,
		"pipelines", knownPipelines,
		"sources", opts.Sources,
	)

	for {
		if err := ctx.Err(); err != nil {
			opts.Logger.Info("worker shutting down", "reason", err)
			return nil
		}

		trigger, err := stateClient.ClaimTriggerFor(ctx, knownPipelines, opts.Sources)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			opts.Logger.Error("claim failed", "err", err)
			sleepOrCancel(ctx, opts.PollInterval)
			continue
		}
		if trigger == nil {
			sleepOrCancel(ctx, opts.PollInterval)
			continue
		}

		opts.Logger.Info(
			"claimed trigger",
			"run_id", trigger.ID,
			"pipeline", trigger.Pipeline,
			"source", trigger.TriggerSource,
		)
		orchestrator.ExecuteClaimedTrigger(ctx, opts, backends, stateClient, trigger)
	}
}
