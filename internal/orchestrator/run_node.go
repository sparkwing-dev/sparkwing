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
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func RunNodeOnce(
	ctx context.Context,
	controllerURL, runID, nodeID, holderID, token string,
	delegate sparkwing.Logger,
	logger *slog.Logger,
	admission *LocalAdmission,
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

	stateHTTP := &http.Client{Timeout: 60 * time.Second}
	stateClient := client.NewWithToken(controllerURL, stateHTTP, token)

	paths, err := DefaultPaths()
	if err != nil {
		return runner.Result{}, fmt.Errorf("resolve paths: %w", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		return runner.Result{}, fmt.Errorf("ensure root: %w", err)
	}
	logsBackend := localLogs{paths: paths}

	run, err := stateClient.GetRunForExecution(ctx, runID)
	if err != nil {
		return runner.Result{}, fmt.Errorf("get run %s: %w", runID, err)
	}
	trigger, err := stateClient.GetTrigger(ctx, runID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return runner.Result{}, fmt.Errorf("get trigger %s: %w", runID, err)
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Pipeline: run.Pipeline})

	if cfg.claimed {
		if shouldRunRemote(trigger) {
			if controllerURL == "" {
				return runner.Result{}, fmt.Errorf(
					"run %s node %s dispatches to a remote runner, which cannot reach this machine's admission daemon socket; set SPARKWING_CONTROLLER_URL to a controller the runner can reach",
					runID, nodeID)
			}
			return runNodeRemote(ctx, trigger, run, controllerURL, cfg.gitcacheURL, cfg.gitcacheGrant,
				runID, nodeID, token, cfg.repoAllowlist, logger)
		}
		return runner.Result{}, fmt.Errorf("run %s names no repository or working-tree source, so there is no pipeline for node %s to build",
			runID, nodeID)
	}
	if shouldRunRemote(trigger) {
		if controllerURL == "" {
			return runner.Result{}, fmt.Errorf(
				"run %s node %s dispatches to a remote runner, which cannot reach this machine's admission daemon socket; set SPARKWING_CONTROLLER_URL to a controller the runner can reach",
				runID, nodeID)
		}
		return runNodeRemote(ctx, trigger, run, controllerURL, cfg.gitcacheURL, cfg.gitcacheGrant,
			runID, nodeID, token, cfg.repoAllowlist, logger)
	}

	art, err := resolveArtifactStoreFromEnv(ctx)
	if err != nil {
		return runner.Result{}, fmt.Errorf("artifact store: %w", err)
	}
	backends := RemoteBackends(stateClient, logsBackend, art, stateHTTP, store.DefaultConcurrencyLease)

	reg, ok := sparkwing.Lookup(run.Pipeline)
	if !ok {
		return runner.Result{}, fmt.Errorf(
			"pipeline %q not registered in this runner image and trigger has no GITHUB_REPOSITORY to clone from",
			run.Pipeline,
		)
	}

	rc := runContextFor(run)
	rc.NoCache, rc.DryRun = noCacheFromEnv(), dryRunFromEnv()
	sparkwing.SetGit(rc.Git)

	invokeArgs, err := checkoutInvokeArgs(run.Pipeline, run.Args)
	if err != nil {
		return runner.Result{}, err
	}
	masker := maskerForInvokeArgs(reg, invokeArgs)
	// safety: no logger here on purpose. Every dispatching process re-plans,
	// so a sink would repeat one authored line once per node.
	plan, err := reg.Invoke(ctx, invokeArgs, rc)
	if err != nil {
		return runner.Result{}, fmt.Errorf("build plan: %w", err)
	}
	ctx = sparkwingruntime.WithJSONResolver(ctx, func(id string) ([]byte, bool) {
		data, err := stateClient.GetNodeOutput(ctx, runID, id)
		if err != nil {
			return nil, false
		}
		return data, true
	})

	ctx = sparkwing.WithSecretResolver(ctx,
		secrets.NewCached(controllerSecretSource(ctx, stateClient, runID), masker).AsResolver())

	ctx = secrets.WithMasker(ctx, masker)
	ctx = sparkwingruntime.WithOIDCTokenSource(ctx, func(ctx context.Context, audience string) (string, error) {
		tok, oerr := stateClient.OIDCToken(ctx, runID, audience)
		if oerr != nil {
			return "", fmt.Errorf("oidc token for run %s: %w", runID, oerr)
		}
		masker.Register(tok.Token)
		return tok.Token, nil
	})

	if in := plan.Inputs(); in != nil {
		ctx = sparkwingruntime.WithInputs(ctx, in)
	}

	if info := podRunnerInfo(); info != nil {
		ctx = sparkwingruntime.WithRunner(ctx, info)
	}

	if sec, serr := rehydratePipelineSecrets(ctx, run.PlanSnapshot, reg); serr != nil {
		logger.Warn("pod: rehydrate pipeline secrets", "err", serr)
	} else if sec != nil {
		ctx = sparkwingruntime.WithPipelineSecrets(ctx, sec)
	}

	var pipelineRefs sparkwing.PipelineResolver = newPipelineRefResolver(stateClient, runID,
		func(_ context.Context, node string, err error) {
			logger.Warn("pipeline_ref audit event append failed",
				"run_id", runID, "node", node, "err", err)
		})
	ctx = sparkwingruntime.WithPipelineResolver(ctx, pipelineRefs)

	childDiagnostics := podChildAwaitDiagnostics{logger: logger}
	childAwait := childAwaitConfig{
		state:       backends.State,
		concurrency: backends.Concurrency,
		parentRunID: runID,
		retryOf:     run.RetryOf,
		masker:      masker,
		diagnostics: childDiagnostics,
		pollFactory: func() (childAwaitPollPolicy, error) {
			return &retryChildAwaitPoll{
				diagnostics: childDiagnostics,
				runID:       runID,
				nodeID:      nodeID,
			}, nil
		},
	}
	ctx = sparkwingruntime.WithPipelineAwaiter(ctx, sparkwing.PipelineAwaiterFunc(childAwait.await))

	node := plan.Job(nodeID)
	var generatorErr error
	if node == nil {
		for _, exp := range plan.Expansions() {
			children, err := invokeGeneratorForPod(ctx, exp)
			if err != nil {
				generatorErr = errors.Join(generatorErr, err)
				continue
			}
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
		return runner.Result{}, errors.Join(fmt.Errorf("node %q not found in plan for %s (static nodes + all ExpandFrom generators exhausted)", nodeID, run.Pipeline), generatorErr)
	}

	if admission != nil {
		ctx = withLocalAdmissionPlan(ctx, plan)
		priority := planPriorityFromSnapshot(run.PlanSnapshot)
		if reservedCtx, ok := admission.attachReservedNode(ctx, runID, priority); ok {
			ctx = reservedCtx
		} else {
			lease, aerr := admission.admitNode(ctx, backends, run.Pipeline, runID, nodeID, node, priority)
			if aerr != nil {
				return runner.Result{}, fmt.Errorf("local admission: %w", aerr)
			}
			defer lease.release()
			ctx = withLocalAdmission(ctx, admission, runID, lease.token, lease.childToken, lease.hostAdmitted, priority, lease.charge)
		}
	}

	r := NewNodeExecutor(backends)
	req := runner.Request{
		RunID:    runID,
		NodeID:   nodeID,
		Pipeline: run.Pipeline,
		Args:     invokeArgs,
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			run.GitSHA, run.GitBranch, "", run.DeclaredRepo, run.RepoURL),
		Trigger:  sparkwing.TriggerInfo{Source: run.TriggerSource},
		Node:     node,
		Delegate: delegate,
	}
	start := time.Now()
	res := r.RunNode(ctx, req)
	if MetricsHook != nil {
		MetricsHook(run.Pipeline, string(res.Outcome), time.Since(start))
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Outcome: string(res.Outcome)})
	return res, nil
}

var MetricsHook func(pipeline, outcome string, d time.Duration)

func runContextFor(run *store.Run) sparkwing.RunContext {
	return sparkwing.RunContext{
		RunID:    run.ID,
		Pipeline: run.Pipeline,
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			run.GitSHA, run.GitBranch, "", run.DeclaredRepo, run.RepoURL),
		Trigger:   sparkwing.TriggerInfo{Source: run.TriggerSource},
		StartedAt: run.StartedAt,
	}
}

func invokeGeneratorForPod(ctx context.Context, exp sparkwing.Expansion) (out []*sparkwing.JobNode, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
			err = fmt.Errorf("generator from %s panicked: %v", exp.Source.ID(), r)
		}
	}()
	return exp.Gen(ctx), nil
}
