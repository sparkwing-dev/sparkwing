package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/otelutil"
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// RunNodeOnce is the shared execution core for cluster-mode node
// runs: fetches run + plan, installs HTTP resolvers, locates the node
// (with ExpandFrom fallback), and invokes InProcessRunner against
// HTTP backends. The runner writes terminal state through the
// controller; the returned Result is for caller-side logging.
//
// holderID is the lock/claim holder id (e.g. "pod:<runID>:<nodeID>"
// or "runner:<hostname>"). token is the bearer for controller + logs;
// empty = no auth header.
func RunNodeOnce(
	ctx context.Context,
	controllerURL, logsURL, runID, nodeID, holderID, token string,
	delegate sparkwing.Logger,
	logger *slog.Logger,
) (runner.Result, error) {
	if logger == nil {
		logger = slog.Default()
	}

	ctx, span := otelutil.Tracer("sparkwing-orchestrator").Start(ctx, "RunNodeOnce")
	defer span.End()
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{RunID: runID, NodeID: nodeID})

	httpClient := &http.Client{
		Timeout: 60 * time.Second,
	}
	stateClient := client.NewWithToken(controllerURL, httpClient, token)

	paths, err := DefaultPaths()
	if err != nil {
		return runner.Result{}, fmt.Errorf("resolve paths: %w", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		return runner.Result{}, fmt.Errorf("ensure root: %w", err)
	}
	var logsBackend LogBackend
	if logsURL != "" {
		logsBackend = NewHTTPLogsWithToken(logsURL, httpClient, token, logger)
	} else {
		logsBackend = localLogs{paths: paths}
	}

	concurrencyBackend := NewHTTPConcurrency(controllerURL, httpClient, token, store.DefaultConcurrencyLease)

	backends := Backends{
		State:       stateClient,
		Logs:        logsBackend,
		Concurrency: concurrencyBackend,
	}

	run, err := stateClient.GetRun(ctx, runID)
	if err != nil {
		return runner.Result{}, fmt.Errorf("get run %s: %w", runID, err)
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Pipeline: run.Pipeline})

	// When the trigger carries GITHUB_REPOSITORY, that repo is the
	// source of truth -- baked-in pipelines must NOT win, since
	// they'd run against the runner pod's empty workdir.
	if shouldRunRemote(ctx, stateClient, runID) {
		return runNodeRemote(ctx, stateClient, run, controllerURL, logsURL, runID, nodeID, token, logger)
	}

	reg, ok := sparkwing.Lookup(run.Pipeline)
	if !ok {
		return runner.Result{}, fmt.Errorf(
			"pipeline %q not registered in this runner image and trigger has no GITHUB_REPOSITORY to clone from",
			run.Pipeline)
	}

	rc := sparkwing.RunContext{
		RunID:    run.ID,
		Pipeline: run.Pipeline,
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			run.GitSHA, run.GitBranch, run.Repo, run.RepoURL),
		Trigger:   sparkwing.TriggerInfo{Source: run.TriggerSource},
		StartedAt: run.StartedAt,
	}
	sparkwing.SetGit(rc.Git)
	// Pre-populate from `secret:"true"` Inputs fields so their values
	// are redacted in every log stream and resolver path.
	masker := secrets.NewMasker()
	for _, v := range reg.SecretValues(run.Args) {
		masker.Register(v)
	}
	plan, err := reg.Invoke(ctx, run.Args, rc)
	if err != nil {
		return runner.Result{}, fmt.Errorf("build plan: %w", err)
	}
	ctx = sparkwing.WithJSONResolver(ctx, func(id string) ([]byte, bool) {
		data, err := stateClient.GetNodeOutput(ctx, runID, id)
		if err != nil {
			return nil, false
		}
		return data, true
	})

	// Pod-side secret resolver. Cached memoizes per-name and registers
	// values with the run's masker so they get redacted in logs.
	httpSource := secrets.SourceFunc(func(name string) (string, bool, error) {
		sec, gerr := stateClient.GetSecret(ctx, name)
		if gerr != nil {
			if errors.Is(gerr, store.ErrNotFound) {
				return "", false, secrets.ErrSecretMissing
			}
			return "", false, gerr
		}
		return sec.Value, sec.Masked, nil
	})
	ctx = sparkwing.WithSecretResolver(ctx,
		secrets.NewCached(httpSource, masker).AsResolver())
	_ = masker

	// Pod-side install of the typed Inputs the registration parsed,
	// so step bodies can read via sparkwing.Inputs[T](ctx) without
	// per-job closure threading.
	if in := plan.Inputs(); in != nil {
		ctx = sparkwing.WithInputs(ctx, in)
	}

	// Pod-side twin of dispatchState.pipelineRef.
	ctx = sparkwing.WithPipelineResolver(ctx, sparkwing.PipelineResolverFunc(
		func(innerCtx context.Context, pipeline, refNode string, maxAge time.Duration) (*sparkwing.ResolvedPipelineRef, error) {
			run, err := stateClient.GetLatestRun(innerCtx, pipeline, []string{"success"}, maxAge)
			if err != nil {
				return nil, fmt.Errorf("no matching run for pipeline %q (maxAge=%s): %w", pipeline, maxAge, err)
			}
			data, err := stateClient.GetNodeOutput(innerCtx, run.ID, refNode)
			if err != nil {
				return nil, fmt.Errorf("get node %s/%s output: %w", run.ID, refNode, err)
			}
			// Best-effort audit event against the consuming node.
			currentNode := sparkwing.NodeFromContext(innerCtx)
			if currentNode != "" {
				payload, _ := json.Marshal(map[string]any{
					"pipeline":        pipeline,
					"node_id":         refNode,
					"source_run_id":   run.ID,
					"max_age_seconds": int64(maxAge.Seconds()),
					"source_finished": run.FinishedAt,
				})
				if evErr := stateClient.AppendEvent(innerCtx, runID, currentNode,
					"pipeline_ref_resolved", payload); evErr != nil {
					logger.Warn("pipeline_ref audit event append failed",
						"run_id", runID, "node", currentNode, "err", evErr)
				}
			}
			return &sparkwing.ResolvedPipelineRef{RunID: run.ID, Data: data}, nil
		}))

	// Cluster-mode equivalent of dispatchState.pipelineAwaiter.
	ctx = sparkwing.WithPipelineAwaiter(ctx, sparkwing.PipelineAwaiterFunc(
		func(innerCtx context.Context, req sparkwing.AwaitRequest) (*sparkwing.ResolvedPipelineRef, error) {
			currentNode := sparkwing.NodeFromContext(innerCtx)

			// Retry-lineage chain: thread the prior run's child
			// trigger into retry_of for skip-passed treatment.
			var childRetryOf string
			if run.RetryOf != "" && currentNode != "" {
				if id, ferr := stateClient.FindSpawnedChildTriggerID(innerCtx, run.RetryOf, currentNode, req.Pipeline); ferr != nil {
					logger.Warn("find prior spawned child for retry chain",
						"run_id", runID, "node", currentNode, "err", ferr)
				} else {
					childRetryOf = id
				}
			}

			childRunID, err := stateClient.EnqueueTrigger(innerCtx,
				req.Pipeline, req.Args, runID, currentNode, childRetryOf,
				"await-pipeline", "", req.Repo, req.Branch)
			if err != nil {
				return nil, fmt.Errorf("enqueue trigger: %w", err)
			}
			if currentNode != "" {
				payload, _ := json.Marshal(map[string]any{
					"pipeline":        req.Pipeline,
					"node_id":         req.NodeID,
					"child_run_id":    childRunID,
					"timeout_seconds": int64(req.Timeout.Seconds()),
				})
				if evErr := stateClient.AppendEvent(innerCtx, runID, currentNode,
					"pipeline_await_spawned", payload); evErr != nil {
					logger.Warn("pipeline_await audit event append failed",
						"run_id", runID, "node", currentNode, "err", evErr)
				}
			}
			pollCtx := innerCtx
			if req.Timeout > 0 {
				var cancel context.CancelFunc
				pollCtx, cancel = context.WithTimeout(innerCtx, req.Timeout)
				defer cancel()
			}
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				run, err := stateClient.GetRun(pollCtx, childRunID)
				if err == nil {
					switch run.Status {
					case "success":
						// Empty NodeID = caller doesn't need output.
						if req.NodeID == "" {
							return &sparkwing.ResolvedPipelineRef{RunID: childRunID}, nil
						}
						data, oerr := stateClient.GetNodeOutput(pollCtx, childRunID, req.NodeID)
						if oerr != nil {
							return nil, fmt.Errorf("get child %s/%s output: %w", childRunID, req.NodeID, oerr)
						}
						return &sparkwing.ResolvedPipelineRef{RunID: childRunID, Data: data}, nil
					case "failed":
						return nil, fmt.Errorf("child run %s failed: %s", childRunID, run.Error)
					case "cancelled":
						return nil, fmt.Errorf("child run %s was cancelled", childRunID)
					}
				}
				select {
				case <-pollCtx.Done():
					return nil, fmt.Errorf("waiting for child %s: %w", childRunID, pollCtx.Err())
				case <-ticker.C:
				}
			}
		}))

	node := plan.Node(nodeID)
	if node == nil {
		for _, exp := range plan.Expansions() {
			children := invokeGeneratorForPod(ctx, exp)
			for _, c := range children {
				if c.ID() == nodeID {
					node = c
					break
				}
			}
			if node != nil {
				break
			}
		}
	}
	if node == nil {
		return runner.Result{}, fmt.Errorf("node %q not found in plan for %s (static nodes + all ExpandFrom generators exhausted)", nodeID, run.Pipeline)
	}

	r := NewInProcessRunner(backends)
	start := time.Now()
	res := r.RunNode(ctx, runner.Request{
		RunID:    runID,
		NodeID:   nodeID,
		Pipeline: run.Pipeline,
		Args:     run.Args,
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			run.GitSHA, run.GitBranch, run.Repo, run.RepoURL),
		Trigger:  sparkwing.TriggerInfo{Source: run.TriggerSource},
		Node:     node,
		Delegate: delegate,
	})
	if MetricsHook != nil {
		MetricsHook(run.Pipeline, string(res.Outcome), time.Since(start))
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Outcome: string(res.Outcome)})
	return res, nil
}

// MetricsHook is set by sparkwing-runner to emit per-node metrics.
// Nil in user pipeline binaries to keep the prometheus dep out.
var MetricsHook func(pipeline, outcome string, d time.Duration)

// runNodeCLI implements `wing run-node <runID> <nodeID>`. One node
// per invocation; orchestrator creates the node row first, this body
// executes it and writes terminal state through the controller.
func runNodeCLI(args []string) error {
	fs := flag.NewFlagSet("run-node", flag.ExitOnError)
	controllerURL := fs.String("controller", ResolveDevEnvURL("SPARKWING_CONTROLLER_URL"),
		"controller base URL (env: SPARKWING_CONTROLLER_URL, falls back to $SPARKWING_HOME/dev.env)")
	logsURL := fs.String("logs", ResolveDevEnvURL("SPARKWING_LOGS_URL"),
		"logs-service URL (env: SPARKWING_LOGS_URL, falls back to $SPARKWING_HOME/dev.env)")
	timeout := fs.Duration("timeout", 0,
		"max wall-clock duration for the node (0 = none; job-level modifiers still apply)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	runID := fs.Arg(0)
	if runID == "" {
		runID = os.Getenv("SPARKWING_RUN_ID")
	}
	nodeID := fs.Arg(1)
	if nodeID == "" {
		nodeID = os.Getenv("SPARKWING_NODE_ID")
	}
	if *controllerURL == "" || runID == "" || nodeID == "" {
		fs.Usage()
		return errors.New("--controller + <runID> + <nodeID> are required (or SPARKWING_CONTROLLER_URL + SPARKWING_RUN_ID + SPARKWING_NODE_ID env)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	holderID := fmt.Sprintf("pod:%s:%s", runID, nodeID)
	token := os.Getenv("SPARKWING_AGENT_TOKEN")
	res, err := RunNodeOnce(ctx, *controllerURL, *logsURL, runID, nodeID, holderID, token,
		selectLocalRenderer(), slog.Default())
	if err != nil {
		return err
	}
	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "node %s/%s failed: %v\n", runID, nodeID, res.Err)
		return res.Err
	}
	fmt.Fprintf(os.Stderr, "node %s/%s outcome=%s\n", runID, nodeID, res.Outcome)
	return nil
}

// invokeGeneratorForPod runs one ExpandFrom generator under panic
// recovery; panics yield an empty slice so the caller tries the next
// expansion.
func invokeGeneratorForPod(ctx context.Context, exp sparkwing.Expansion) (out []*sparkwing.Node) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
		}
	}()
	return exp.Gen(ctx)
}
