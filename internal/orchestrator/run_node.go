package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwingcache"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

	transports := nodeTransportsFor(cfg, controllerURL, token)
	defer transports.close()
	httpClient := transports.plain
	stateHTTP := transports.state
	stateClient := client.NewWithToken(transports.stateURL, stateHTTP, transports.stateToken)

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

	run, err := stateClient.GetRunForExecution(ctx, runID)
	if err != nil {
		return runner.Result{}, fmt.Errorf("get run %s: %w", runID, err)
	}
	trigger, err := stateClient.GetTrigger(ctx, runID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return runner.Result{}, fmt.Errorf("get trigger %s: %w", runID, err)
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Pipeline: run.Pipeline})

	if cfg.claimed && !cfg.brokeredChild {
		if shouldRunRemote(trigger, false) {
			if controllerURL == "" {
				return runner.Result{}, fmt.Errorf(
					"run %s node %s dispatches to a remote runner, which cannot reach this machine's admission daemon socket; set SPARKWING_CONTROLLER_URL to a controller the runner can reach",
					runID, nodeID)
			}
			return runNodeRemote(ctx, trigger, run, controllerURL, logsURL, cfg.gitcacheURL, cfg.gitcacheToken,
				runID, nodeID, token, logger)
		}
		return runNodeIsolatedFn(ctx, controllerURL, logsURL, runID, nodeID, token, logger)
	}
	if shouldRunRemote(trigger, cfg.brokeredChild) {
		if controllerURL == "" {
			return runner.Result{}, fmt.Errorf(
				"run %s node %s dispatches to a remote runner, which cannot reach this machine's admission daemon socket; set SPARKWING_CONTROLLER_URL to a controller the runner can reach",
				runID, nodeID)
		}
		return runNodeRemote(ctx, trigger, run, controllerURL, logsURL, cfg.gitcacheURL, cfg.gitcacheToken,
			runID, nodeID, token, logger)
	}

	var art storage.ArtifactStore
	var localSecrets secrets.Source
	if cfg.coordinated {
		// safety: rebuild local surfaces; a laptop run's secrets and artifact
		// store do not belong to the pod's controller-backed profile.
		var profileLogs LogBackend
		localSecrets, art, profileLogs, err = coordinatedChildSurfaces(ctx, run.Pipeline)
		if err != nil {
			return runner.Result{}, err
		}
		if profileLogs != nil {
			logsBackend = profileLogs
		}
	} else if cfg.brokeredChild && cfg.brokerArtifact {
		art = sparkwingcache.New(controllerURL, token, httpClient)
	} else {
		art, err = resolveArtifactStoreFromEnv(ctx)
		if err != nil {
			return runner.Result{}, fmt.Errorf("artifact store: %w", err)
		}
	}
	backends := RemoteBackends(stateClient, logsBackend, art, stateHTTP, store.DefaultConcurrencyLease)

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
		NoCache:   noCacheFromEnv(),
		DryRun:    dryRunFromEnv(),
	}
	sparkwing.SetGit(rc.Git)

	invokeArgs := checkoutInvokeArgs(run.Pipeline, run.Args, logger)
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

	source := localSecrets
	if source == nil {
		source = secrets.SourceFunc(func(name string) (string, bool, error) {
			sec, gerr := stateClient.GetSecretForRun(ctx, name, runID)
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

	ctx = secrets.WithMasker(ctx, masker)

	if in := plan.Inputs(); in != nil {
		ctx = sparkwingruntime.WithInputs(ctx, in)
	}

	// safety: propagate the dispatcher's resolved args or an external node
	// silently falls back to schema defaults.
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

	ctx = sparkwingruntime.WithPipelineResolver(ctx, newPipelineRefResolver(stateClient, runID,
		func(_ context.Context, node string, err error) {
			logger.Warn("pipeline_ref audit event append failed",
				"run_id", runID, "node", node, "err", err)
		}))

	childDiagnostics := podChildAwaitDiagnostics{logger: logger}
	childAwait := childAwaitConfig{
		state:       stateClient,
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
		priority := planPriorityFromSnapshot(run.PlanSnapshot)
		if reservedCtx, ok := admission.attachReservedNode(ctx, priority); ok {
			ctx = reservedCtx
		} else {
			lease, aerr := admission.admitNode(ctx, backends, run.Pipeline, runID, nodeID, node, priority)
			if aerr != nil {
				return runner.Result{}, fmt.Errorf("local admission: %w", aerr)
			}
			defer lease.release()
			ctx = withLocalAdmission(ctx, admission, lease.token, lease.childToken, lease.hostAdmitted, priority)
		}
	}

	if cfg.coordinated {
		ctx, err = installStepControlsFromEnv(ctx, plan)
		if err != nil {
			return runner.Result{}, err
		}
		if !cfg.claimed && !cfg.brokeredChild {
			ctx = withLocalExecution(ctx)
		}
	}

	r := NewNodeExecutor(backends)
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

var MetricsHook func(pipeline, outcome string, d time.Duration)

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
	apiSocket := os.Getenv(wingwire.APISocketEnv)
	if (*controllerURL == "" && apiSocket == "") || runID == "" || nodeID == "" {
		fs.Usage()
		return errors.New("--controller (or " + wingwire.APISocketEnv + ") + <runID> + <nodeID> are required (or SPARKWING_CONTROLLER_URL + SPARKWING_RUN_ID + SPARKWING_NODE_ID env)")
	}

	// safety: the supervisor records a killed node's outcome, not the node, so
	// SIGTERM keeping its default action is intended. The forwarder reaps only the
	// step sessions, which the SDK isolates, so no group kill aimed at the node
	// reaches them.
	procgroup.ForwardTerminationToOwned()
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
	brokeredChild := os.Getenv(remoteExecutionCapabilityInputEnv) == "1"
	if brokeredChild {
		capability, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil {
			return fmt.Errorf("read execution capability: %w", err)
		}
		token = string(bytes.TrimSpace(capability))
		if len(token) < 32 || len(capability) > 4096 {
			return errors.New("execution capability input is invalid")
		}
		runOpts = append(runOpts, brokeredExecutionChild(os.Getenv(remoteBrokeredArtifactEnv) == "1"))
	}
	// safety: RunNodeCommand reads the same claim variables for a Job's pod,
	// where the process already is the isolated execution; here it supervises
	// one, so a claim means handing the node to a child through the broker.
	if os.Getenv(remoteBrokeredClaimEnv) == "1" {
		holderID = "brokered-execution"
		runOpts = append(runOpts, func(c *runNodeConfig) {
			c.claimed = true
			c.claimFence = store.NodeClaimFence{
				HolderID: "brokered-execution", MembershipID: "brokered-membership",
				ReservationID: "brokered-reservation", ClaimGeneration: 1,
			}
		})
	} else if claimedHolder := os.Getenv("SPARKWING_NODE_CLAIM_HOLDER"); claimedHolder != "" {
		holderID = claimedHolder
		generation, err := strconv.ParseInt(os.Getenv("SPARKWING_NODE_CLAIM_GENERATION"), 10, 64)
		if err != nil || generation < 1 {
			return errors.New("SPARKWING_NODE_CLAIM_GENERATION must name the awarded claim generation")
		}
		runOpts = append(runOpts, func(c *runNodeConfig) {
			c.claimed = true
			c.claimFence = store.NodeClaimFence{
				ClaimGeneration: generation,
				MembershipID:    os.Getenv("SPARKWING_NODE_CLAIM_MEMBERSHIP"),
				ReservationID:   os.Getenv("SPARKWING_NODE_CLAIM_RESERVATION"),
			}
		})
	}
	if apiSocket != "" {
		runOpts = append(runOpts, OverAPISocket(apiSocket))
		token = ""
	}
	if *coordinated {
		holderID = fmt.Sprintf("node:%s:%s", runID, nodeID)
		runOpts = append(runOpts, Coordinated())
		var abandon context.CancelFunc
		ctx, abandon = context.WithCancel(ctx)
		defer abandon()
		defer WatchParentLiveness(abandon)()
	}
	if brokeredChild {
		for name := range remoteExecutionPrivateEnv {
			_ = os.Unsetenv(name)
		}
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

func coordinatedExitStatus(runID, nodeID string, res runner.Result) error {
	if res.Err == nil {
		return nil
	}
	return fmt.Errorf("node %s/%s failed; its terminal row carries the reason", runID, nodeID)
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

type nodeTransports struct {
	stateURL   string
	stateToken string
	state      *http.Client
	plain      *http.Client
	close      func()
}

// safety: handing the logs backend the socket transport would dial api.sock
// for every record whatever host the logs URL names, and the daemon serves no
// log route, so the records would 404 and be dropped.
func nodeTransportsFor(cfg runNodeConfig, controllerURL, token string) nodeTransports {
	plain := &http.Client{Timeout: 60 * time.Second}
	out := nodeTransports{
		stateURL:   controllerURL,
		stateToken: token,
		state:      plain,
		plain:      plain,
		close:      func() {},
	}
	if cfg.apiSocket == "" {
		return out
	}
	socket := NewAPISocketClient(cfg.apiSocket)
	out.state = socket
	out.stateURL = HostedAPIBaseURL
	out.stateToken = ""
	out.close = socket.CloseIdleConnections
	return out
}
