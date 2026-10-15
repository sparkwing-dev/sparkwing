package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func RunNodeOnce(
	ctx context.Context,
	controllerURL, runID, nodeID, holderID, token string,
	logger *slog.Logger,
	opts ...RunNodeOption,
) (runner.Result, error) {
	var cfg runNodeConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.claimed {
		if cfg.claimFence.ClaimGeneration < 1 {
			return runner.Result{}, errors.New("claimed node is missing its claim-generation fence")
		}
		cfg.claimFence.HolderID = holderID
		ctx = withNodeClaimHolder(ctx, holderID)
		ctx = store.WithNodeClaimFence(ctx, cfg.claimFence)
	} else if cfg.claimFence.HolderID != "" && cfg.claimFence.ClaimGeneration > 0 {
		// safety: this process is already the isolated execution, so the claim
		// fences its own writes rather than handing the node to a child.
		ctx = withNodeClaimHolder(ctx, cfg.claimFence.HolderID)
		ctx = store.WithNodeClaimFence(store.WithoutClaimFences(ctx), cfg.claimFence)
	}
	if logger == nil {
		logger = slog.Default()
	}

	ctx, span := otelutil.Tracer("sparkwing-orchestrator").Start(ctx, "RunNodeOnce")
	defer span.End()
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{RunID: runID, NodeID: nodeID})

	stateClient := client.NewWithToken(controllerURL, &http.Client{Timeout: 60 * time.Second}, token)
	run, err := stateClient.GetRunForExecution(ctx, runID)
	if err != nil {
		return runner.Result{}, fmt.Errorf("get run %s: %w", runID, err)
	}
	trigger, err := stateClient.GetTrigger(ctx, runID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return runner.Result{}, fmt.Errorf("get trigger %s: %w", runID, err)
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Pipeline: run.Pipeline})

	if !shouldRunRemote(trigger) {
		return runner.Result{}, fmt.Errorf("run %s names no repository or working-tree source, so there is no pipeline for node %s to build",
			runID, nodeID)
	}
	if controllerURL == "" {
		return runner.Result{}, fmt.Errorf(
			"run %s node %s dispatches to a remote runner, which cannot reach this machine's admission daemon socket; set SPARKWING_CONTROLLER_URL to a controller the runner can reach",
			runID, nodeID)
	}
	host := runNodeRemote
	if cfg.hostRemote != nil {
		host = cfg.hostRemote
	}
	start := time.Now()
	res, err := host(ctx, trigger, run, controllerURL,
		runID, nodeID, token, cfg.repoAllowlist, logger)
	if err != nil {
		return res, err
	}
	if MetricsHook != nil {
		MetricsHook(run.Pipeline, string(res.Outcome), time.Since(start))
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Outcome: string(res.Outcome)})
	return res, nil
}

var MetricsHook func(pipeline, outcome string, d time.Duration)
