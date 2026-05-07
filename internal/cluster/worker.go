package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
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

	// State + Concurrency are HTTP-backed ( /). The
	// runner-pod trust boundary requires that any *store.Store
	// reachable from Backends collapse the controller's privilege
	// boundary the moment user .inline() code runs in this process.
	// Mirrors orchestrator.HandleClaimedTrigger's wiring (worker.go
	// "Concurrency must go through the controller" block).
	//
	// The dummy local store exists ONLY so LocalBackends can hand back
	// a localLogs value for the no-LogsURL fallback. State and
	// Concurrency are overwritten with HTTP variants below; the local
	// store is otherwise unreferenced and is closed on return.
	dummyStore, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("open local store for logs fallback: %w", err)
	}
	defer dummyStore.Close()
	local := orchestrator.LocalBackends(paths, dummyStore)

	stateClient := client.NewWithToken(opts.ControllerURL, opts.HTTPClient, opts.Token)

	var logsBackend orchestrator.LogBackend = local.Logs
	switch {
	case opts.LogStore != nil:
		logsBackend = orchestrator.NewLogStoreBackend(opts.LogStore, opts.Logger)
	case opts.LogsURL != "":
		logsBackend = orchestrator.NewHTTPLogsWithToken(opts.LogsURL, opts.HTTPClient, opts.Token, opts.Logger)
	}

	// Concurrency MUST go through the controller -- cache hits, slot
	// holders, and waiter resolution have to be shared across every
	// runner process, and direct *store.Store access from a runner
	// would let .inline() pipeline code reach the controller's
	// authoritative SQLite. See / and
	// orchestrator/cluster_safety_test.go for the privilege-
	// escalation rationale.
	backends := orchestrator.Backends{
		State:       stateClient,
		Logs:        logsBackend,
		Concurrency: orchestrator.NewHTTPConcurrency(opts.ControllerURL, opts.HTTPClient, opts.Token, store.DefaultConcurrencyLease),
	}

	// Pipeline filter (FOLLOWUPS #8a phase 2). Auto-advertise the
	// pipeline names registered in this binary so the controller
	// hands out only triggers we can actually run. Cross-repo
	// workers don't need to duplicate the list in a flag; whatever's
	// in .sparkwing/main.go imports flows through here.
	//
	// Empty result (no pipelines registered) falls back to the
	// pre-filter "accept any" behavior -- matters for one-off tool
	// binaries that import the orchestrator but not the pipeline
	// registry.
	knownPipelines := sparkwing.Registered()
	opts.Logger.Info("worker started",
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
			// Controller might be unreachable (startup race, restart,
			// network glitch). Don't crash; sleep and retry.
			opts.Logger.Error("claim failed", "err", err)
			sleepOrCancel(ctx, opts.PollInterval)
			continue
		}
		if trigger == nil {
			// Queue empty; back off.
			sleepOrCancel(ctx, opts.PollInterval)
			continue
		}

		opts.Logger.Info("claimed trigger",
			"run_id", trigger.ID,
			"pipeline", trigger.Pipeline,
			"source", trigger.TriggerSource,
		)
		orchestrator.ExecuteClaimedTrigger(ctx, opts, backends, stateClient, trigger)
	}
}
