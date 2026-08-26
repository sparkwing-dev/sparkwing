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
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
//
// admission is non-nil only for a registered runner on a box that also
// owns a local admission daemon: the claimed node is submitted to that
// daemon and held under a lease for its whole execution, exactly like a
// local run, so controller work and local work share one arbiter. In a
// Kubernetes pod the scheduler already admitted the work, so the pod's
// run-node entrypoint passes nil and the daemon is never engaged.
//
// Options carry the execution mode. The default is the pod contract
// above. [Coordinated] switches to the local contract, where the
// dispatcher that spawned this process already resolved cache,
// concurrency admission, and SkipIf before deciding to spawn at all.
func RunNodeOnce(
	ctx context.Context,
	controllerURL, logsURL, runID, nodeID, holderID, token string,
	delegate sparkwing.Logger,
	logger *slog.Logger,
	admission *LocalAdmission,
	opts ...RunNodeOption,
) (runner.Result, error) {
	var cfg runNodeConfig
	for _, opt := range opts {
		opt(&cfg)
	}
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

	// Execution read: run.Args seeds the masker below and is handed to
	// reg.Invoke and the runner.Request. The plain GetRun redacts.
	run, err := stateClient.GetRunForExecution(ctx, runID)
	if err != nil {
		return runner.Result{}, fmt.Errorf("get run %s: %w", runID, err)
	}
	trigger, err := stateClient.GetTrigger(ctx, runID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return runner.Result{}, fmt.Errorf("get trigger %s: %w", runID, err)
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Pipeline: run.Pipeline})

	if shouldRunRemote(trigger) {
		return runNodeRemote(ctx, trigger, run, controllerURL, logsURL, runID, nodeID, token, logger)
	}

	var art storage.ArtifactStore
	var localSecrets secrets.Source
	if cfg.coordinated {
		// safety: the dispatcher and this process are the same binary on the
		// same machine reading the same project, so the child rebuilds
		// the run's own surfaces rather than borrowing the pod's
		// controller-backed ones: a laptop run's secrets live in a
		// dotenv file the controller has never seen, and its artifact
		// store is the one the dispatcher's profile named.
		var profileLogs LogBackend
		localSecrets, art, profileLogs, err = coordinatedChildSurfaces(ctx, run.Pipeline)
		if err != nil {
			// safety: the inner errors already name which surface failed.
			return runner.Result{}, err
		}
		if profileLogs != nil {
			logsBackend = profileLogs
		}
	} else {
		art, err = resolveArtifactStoreFromEnv(ctx)
		if err != nil {
			return runner.Result{}, fmt.Errorf("artifact store: %w", err)
		}
	}
	backends := RemoteBackends(stateClient, logsBackend, art, httpClient, store.DefaultConcurrencyLease)

	reg, ok := sparkwing.Lookup(run.Pipeline)
	if !ok {
		return runner.Result{}, fmt.Errorf(
			"pipeline %q not registered in this runner image and trigger has no GITHUB_REPOSITORY to clone from",
			run.Pipeline,
		)
	}

	rc := sparkwing.RunContext{
		RunID:    run.ID,
		Pipeline: run.Pipeline,
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			run.GitSHA, run.GitBranch, "", run.Repo, run.RepoURL),
		Trigger:   sparkwing.TriggerInfo{Source: run.TriggerSource},
		StartedAt: run.StartedAt,
	}
	sparkwing.SetGit(rc.Git)
	// run.Args carries only the operator's explicit layer; the project's
	// defaults.args and the pipeline entry's args: block are re-read from
	// the checkout this node compiled out of, so the pod plans from the
	// same merged set the local plan used. See checkoutInvokeArgs. The
	// masker is seeded from the merge for the same reason it is on the
	// local path: a yaml-supplied `secret:"true"` value the masker never
	// saw is a value the node log persists in the clear.
	invokeArgs := checkoutInvokeArgs(run.Pipeline, run.Args, logger)
	masker := maskerForInvokeArgs(reg, invokeArgs)
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

	source := localSecrets
	if source == nil {
		source = secrets.SourceFunc(func(name string) (string, bool, error) {
			sec, gerr := stateClient.GetSecret(ctx, name)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return "", false, secrets.ErrSecretMissing
				}
				return "", false, gerr
			}
			return sec.Value, sec.Masked, nil
		})
	}
	ctx = sparkwing.WithSecretResolver(ctx,
		secrets.NewCached(source, masker).AsResolver())
	// The masker has to reach the node log wrapper the same way it does
	// on the local path (RunLocal, replay): through the context. Without
	// this the cluster/pod path resolves secrets into a masker nothing
	// ever reads, and node logs persist raw secret values.
	ctx = secrets.WithMasker(ctx, masker)

	if in := plan.Inputs(); in != nil {
		ctx = sparkwingruntime.WithInputs(ctx, in)
	}

	// safety: the dispatcher installs this from the same plan (newDispatchState).
	// Without it every sparkwing.ArgOrDefault call in a node running
	// outside the dispatcher's process reads no resolved-args map and
	// answers with the schema default, silently discarding the operator's
	// value.
	if ra := plan.ResolvedArgs(); ra != nil {
		ctx = sparkwingruntime.WithResolvedArgs(ctx, ra)
	}

	if info := podRunnerInfo(); info != nil {
		ctx = sparkwingruntime.WithRunner(ctx, info)
	}

	if sec, serr := rehydratePipelineSecrets(ctx, run.PlanSnapshot, reg); serr != nil {
		logger.Warn("pod: rehydrate pipeline secrets", "err", serr)
	} else if sec != nil {
		ctx = sparkwingruntime.WithPipelineSecrets(ctx, sec)
	}

	ctx = sparkwingruntime.WithPipelineResolver(ctx, sparkwing.PipelineResolverFunc(
		func(innerCtx context.Context, pipeline, refNode string, maxAge time.Duration) (*sparkwing.ResolvedPipelineRef, error) {
			run, err := stateClient.GetLatestRun(innerCtx, pipeline, []string{"success"}, maxAge)
			if err != nil {
				return nil, fmt.Errorf("no matching run for pipeline %q (maxAge=%s): %w", pipeline, maxAge, err)
			}
			data, err := stateClient.GetNodeOutput(innerCtx, run.ID, refNode)
			if err != nil {
				return nil, fmt.Errorf("get node %s/%s output: %w", run.ID, refNode, err)
			}
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
		},
	))

	ctx = sparkwingruntime.WithPipelineAwaiter(ctx, sparkwing.PipelineAwaiterFunc(
		func(innerCtx context.Context, req sparkwing.AwaitRequest) (*sparkwing.ResolvedPipelineRef, error) {
			currentNode := sparkwing.NodeFromContext(innerCtx)

			var childRetryOf string
			if run.RetryOf != "" && currentNode != "" {
				if id, ferr := stateClient.FindSpawnedChildTriggerID(innerCtx, run.RetryOf, currentNode, req.Pipeline); ferr != nil {
					logger.Warn("find prior spawned child for retry chain",
						"run_id", runID, "node", currentNode, "err", ferr)
				} else {
					childRetryOf = id
				}
			}

			childRunID, err := stateClient.EnqueueTriggerWithEnv(innerCtx,
				req.Pipeline, req.Args, runID, currentNode, childRetryOf,
				"await-pipeline", "", req.Repo, req.Branch, nil)
			if err != nil {
				return nil, fmt.Errorf("enqueue trigger: %w", err)
			}
			startedAt := time.Now()
			emitChildFinish := func(status, errMsg string) {
				if currentNode == "" {
					return
				}
				attrs := map[string]any{
					"child_run_id": childRunID,
					"pipeline":     req.Pipeline,
					"status":       status,
					"duration_ms":  time.Since(startedAt).Milliseconds(),
				}
				if errMsg != "" {
					attrs["error"] = errMsg
				}
				payload, _ := json.Marshal(attrs)
				payload = maskEventPayload(masker, payload)
				if evErr := stateClient.AppendEvent(context.WithoutCancel(innerCtx), runID, currentNode,
					"child_run_finish", payload); evErr != nil {
					logger.Warn("child_run_finish audit event append failed",
						"run_id", runID, "node", currentNode, "err", evErr)
				}
			}

			if currentNode != "" {
				payload, _ := json.Marshal(map[string]any{
					"child_run_id":    childRunID,
					"pipeline":        req.Pipeline,
					"node_id":         req.NodeID,
					"args":            req.Args,
					"timeout_seconds": int64(req.Timeout.Seconds()),
				})
				payload = maskEventPayload(masker, payload)
				if evErr := stateClient.AppendEvent(innerCtx, runID, currentNode,
					"child_run_start", payload); evErr != nil {
					logger.Warn("child_run_start audit event append failed",
						"run_id", runID, "node", currentNode, "err", evErr)
				}
			}
			resumeProgressTimeout := pauseProgressTimeout(innerCtx)
			defer resumeProgressTimeout()
			pollCtx := innerCtx
			parentCtx := nodeParentContextFromContext(innerCtx)
			if req.Timeout > 0 {
				var cancel context.CancelFunc
				pollCtx, cancel = context.WithTimeout(innerCtx, req.Timeout)
				defer cancel()
			}
			timeoutPausedForAdmission := false
			timeoutAdjustedForAdmission := false
			var admissionStatusErr error
			nodeTimeout := nodeTimeoutControllerFromContext(innerCtx)
			var admissionMu sync.Mutex
			updateTimeoutForAdmission := func(statusCtx context.Context) bool {
				if req.Timeout > 0 || nodeTimeout == nil || nodeTimeoutDurationFromContext(innerCtx) <= 0 {
					return false
				}
				if statusCtx.Err() != nil {
					return false
				}
				admission, statusErr := childPlanAdmissionStatusForRun(statusCtx, stateClient, backends.Concurrency, childRunID)
				admissionMu.Lock()
				defer admissionMu.Unlock()
				if timeoutAdjustedForAdmission {
					return false
				}
				if statusErr != nil {
					if timeoutPausedForAdmission {
						admissionStatusErr = statusErr
					}
					return false
				}
				switch admission.Status {
				case childPlanAdmissionQueued:
					if timeoutPausedForAdmission {
						return true
					}
					if nodeTimeout.pauseAt(admission.QueuedAt) {
						timeoutPausedForAdmission = true
						logger.Info("child run queued for plan admission; pausing parent node timeout until admission",
							"run_id", runID, "node", currentNode, "child_run_id", childRunID, "pipeline", req.Pipeline)
						return true
					}
				case childPlanAdmissionAdmitted:
					if timeoutPausedForAdmission {
						if nodeTimeout.resumeAt(admission.AdmittedAt) {
							timeoutPausedForAdmission = false
							timeoutAdjustedForAdmission = true
							logger.Info("child run left plan admission; parent node timeout resumed",
								"run_id", runID, "node", currentNode, "child_run_id", childRunID, "pipeline", req.Pipeline)
							return true
						}
						return false
					}
					if admission.QueuedAt.IsZero() || admission.AdmittedAt.IsZero() {
						return false
					}
					if nodeTimeout.accountCompletedAdmission(admission.QueuedAt, admission.AdmittedAt) {
						timeoutAdjustedForAdmission = true
						logger.Info("child run completed plan admission; parent node timeout adjusted",
							"run_id", runID, "node", currentNode, "child_run_id", childRunID, "pipeline", req.Pipeline)
						return true
					}
				}
				return false
			}
			admissionPauseActive := func() bool {
				admissionMu.Lock()
				defer admissionMu.Unlock()
				return timeoutPausedForAdmission
			}
			currentAdmissionStatusErr := func() error {
				admissionMu.Lock()
				defer admissionMu.Unlock()
				return admissionStatusErr
			}
			admissionDeadlineHandled := func() bool {
				inspectCtx, cancel := context.WithTimeout(parentCtx, childAdmissionInspectorTimeout)
				defer cancel()
				return updateTimeoutForAdmission(inspectCtx)
			}
			if nodeTimeout != nil && req.Timeout == 0 {
				clearInspector := nodeTimeout.setDeadlineInspector(admissionDeadlineHandled)
				defer clearInspector()
			}
			for {
				updateTimeoutForAdmission(parentCtx)
				if statusErr := currentAdmissionStatusErr(); statusErr != nil {
					emitChildFinish("failed", statusErr.Error())
					return nil, fmt.Errorf("child %s plan admission status: %w", childRunID, statusErr)
				}
				run, err := stateClient.GetRun(pollCtx, childRunID)
				if err == nil {
					switch run.Status {
					case "success":
						updateTimeoutForAdmission(parentCtx)
						if err := pollCtx.Err(); err != nil {
							emitChildFinish("timeout", err.Error())
							return nil, fmt.Errorf("waiting for child %s: %w", childRunID, err)
						}
						if deadline, ok := pollCtx.Deadline(); ok && time.Now().After(deadline) {
							emitChildFinish("timeout", context.DeadlineExceeded.Error())
							return nil, fmt.Errorf("waiting for child %s: %w", childRunID, context.DeadlineExceeded)
						}
						emitChildFinish("success", "")
						if req.NodeID == "" {
							return &sparkwing.ResolvedPipelineRef{RunID: childRunID}, nil
						}
						data, oerr := stateClient.GetNodeOutput(pollCtx, childRunID, req.NodeID)
						if oerr != nil {
							return nil, fmt.Errorf("get child %s/%s output: %w", childRunID, req.NodeID, oerr)
						}
						return &sparkwing.ResolvedPipelineRef{RunID: childRunID, Data: data}, nil
					case "failed":
						emitChildFinish("failed", run.Error)
						return nil, fmt.Errorf("child run %s failed: %s", childRunID, run.Error)
					case "cancelled":
						emitChildFinish("cancelled", "")
						return nil, fmt.Errorf("child run %s was cancelled", childRunID)
					}
				}
				updateTimeoutForAdmission(parentCtx)
				if statusErr := currentAdmissionStatusErr(); statusErr != nil {
					emitChildFinish("failed", statusErr.Error())
					return nil, fmt.Errorf("child %s plan admission status: %w", childRunID, statusErr)
				}
				select {
				case <-pollCtx.Done():
					updateTimeoutForAdmission(parentCtx)
					emitChildFinish("timeout", pollCtx.Err().Error())
					return nil, fmt.Errorf("waiting for child %s: %w", childRunID, pollCtx.Err())
				case <-time.After(childAwaitPollInterval(pollCtx, admissionPauseActive())):
				}
			}
		},
	))

	node := plan.Job(nodeID)
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

	if admission != nil {
		lease, aerr := admission.admitNode(ctx, backends, run.Pipeline, runID, nodeID, node, planPriorityFromSnapshot(run.PlanSnapshot))
		if aerr != nil {
			return runner.Result{}, fmt.Errorf("local admission: %w", aerr)
		}
		defer lease.release()
	}

	if cfg.coordinated {
		ctx, err = installStepControlsFromEnv(ctx, plan)
		if err != nil {
			return runner.Result{}, err
		}
	}

	r := NewInProcessRunner(backends)
	// safety: this process is the only thing that can serve a SpawnNode
	// call in the node it is about to run. The dispatcher's handler
	// splices the child into a live plan object that exists only in the
	// dispatcher's memory, and a pod has no dispatcher to ask at all.
	ctx = sparkwingruntime.WithSpawnHandler(ctx, newNodeSpawnHandler(
		r, backends, plan, runID, run.Pipeline, nodeID, delegate,
		nodeProcessPipelineRequires(run.Pipeline, logger)))
	req := runner.Request{
		RunID:    runID,
		NodeID:   nodeID,
		Pipeline: run.Pipeline,
		Args:     invokeArgs,
		Git: sparkwing.NewGit(sparkwing.CurrentRuntime().WorkDir,
			run.GitSHA, run.GitBranch, "", run.Repo, run.RepoURL),
		Trigger:  sparkwing.TriggerInfo{Source: run.TriggerSource},
		Node:     node,
		Delegate: delegate,
	}
	start := time.Now()
	var res runner.Result
	if cfg.coordinated {
		res = r.executeCoordinated(ctx, req)
	} else {
		res = r.RunNode(ctx, req)
	}
	if MetricsHook != nil {
		MetricsHook(run.Pipeline, string(res.Outcome), time.Since(start))
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Outcome: string(res.Outcome)})
	return res, nil
}

// MetricsHook is set by sparkwing-runner to emit per-node metrics.
// Nil in user pipeline binaries to keep the prometheus dep out.
var MetricsHook func(pipeline, outcome string, d time.Duration)

// runNodeCLI implements the pipeline binary's `run-node <runID> <nodeID>` entrypoint. One node
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
	coordinated := fs.Bool("coordinated", false,
		"a local dispatcher owns this node's cache, concurrency, and SkipIf decisions; execute the body only")
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

	// safety: SIGINT only, and SIGTERM must stay unhandled. SIGTERM is how a
	// node process is stopped -- by `runs bounce`, by a cancelled run, by
	// a pod's own termination -- and the guarantee those paths rest on is
	// that the child dies without writing a terminal row: the supervisor
	// decides what the kill meant. A handler here would let a bounced node
	// record an outcome mid-bounce, which is the one thing a bounce must
	// never produce. See TestNodeEntrypoints_DoNotHandleSIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	holderID := fmt.Sprintf("pod:%s:%s", runID, nodeID)
	token := os.Getenv("SPARKWING_AGENT_TOKEN")
	var runOpts []RunNodeOption
	if *coordinated {
		holderID = fmt.Sprintf("node:%s:%s", runID, nodeID)
		runOpts = append(runOpts, Coordinated())
		var abandon context.CancelFunc
		ctx, abandon = context.WithCancel(ctx)
		defer abandon()
		defer WatchParentLiveness(abandon)()
	}
	res, err := RunNodeOnce(ctx, *controllerURL, *logsURL, runID, nodeID, holderID, token,
		selectLocalRenderer(), slog.Default(), nil, runOpts...)
	if err != nil {
		return err
	}
	if *coordinated {
		return coordinatedExitStatus(runID, nodeID, res)
	}
	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "node %s/%s failed: %v\n", runID, nodeID, res.Err)
		return res.Err
	}
	fmt.Fprintf(os.Stderr, "node %s/%s outcome=%s\n", runID, nodeID, res.Outcome)
	return nil
}

// coordinatedExitStatus reports a spawned node's outcome to its
// dispatcher without printing the node's own error text.
//
// The masker that redacts a node's logs is installed inside the node
// log wrapper, so anything written straight to this process's stderr
// goes out unredacted -- and the dispatcher relays stderr into the
// run's delegate and its envelope file. A failure message carrying a
// resolved secret would therefore be masked in the node log and naked
// on the operator's terminal. The dispatcher does not need the text
// anyway: it reads the node's terminal row, where the masked message
// already is. Only the non-zero exit has to survive, for the case
// where the row write itself failed.
func coordinatedExitStatus(runID, nodeID string, res runner.Result) error {
	if res.Err == nil {
		return nil
	}
	return fmt.Errorf("node %s/%s failed; its terminal row carries the reason", runID, nodeID)
}

// invokeGeneratorForPod runs one ExpandFrom generator under panic
// recovery; panics yield an empty slice so the caller tries the next
// expansion.
func invokeGeneratorForPod(ctx context.Context, exp sparkwing.Expansion) (out []*sparkwing.JobNode) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
		}
	}()
	return exp.Gen(ctx)
}
