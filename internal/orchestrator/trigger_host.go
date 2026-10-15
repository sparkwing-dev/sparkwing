package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/internal/runflags"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TriggerHost is one claimed trigger's run, hosted by the runner that claimed
// it.
type TriggerHost struct {
	// ControllerURL and Token are the runner's own; pipeline code never
	// receives the token.
	ControllerURL, Token string
	Trigger              *store.Trigger
	// Entry starts the pipeline's SDK processes, and Dir is the repository
	// root they run in.
	Entry pipelineentry.Command
	Dir   string
	// Runner dispatches each node, as a Kubernetes Job or to a warm runner.
	// Nil hosts each node in this process under a claim of its own.
	Runner   runner.Runner
	Engine   nodeproto.EngineIdentity
	Identity nodeproto.RunnerIdentity
	Logger   *slog.Logger
}

var errTriggerClaimEnded = errors.New("the trigger claim ended, so the run stopped")

// HostTriggerRun plans h's run in a short-lived process of its pipeline,
// runs the scheduler, dispatches each node, and finishes the trigger once
// the run row is terminal. An error means no run row was recorded. The caller
// renews the trigger claim, as [HeartbeatTrigger] does, and cancels ctx with a
// [nodehost.CancelError] for an operator's cancel; a ctx cancelled with no
// cause, as a runner shutting down is, leaves the run and trigger to the
// claim's lease.
//
// safety: plan code and every closure run in processes with the minimal
// environment and only their node's route bearer; the runner's token stays in
// this process, and a node's code reaches the controller only under the
// claim of the host that runs it.
func HostTriggerRun(ctx context.Context, h TriggerHost) error {
	logger := cmp.Or(h.Logger, slog.Default())
	trig := h.Trigger
	ctx = DispatchContext(ctx, trig)
	c := client.NewWithToken(h.ControllerURL, nil, h.Token)
	requires, err := PipelineRequires(h.Dir, trig.Pipeline)
	if err != nil {
		return err
	}
	args, guards, err := checkoutRunArgs(h.Dir, trig.Pipeline, triggerHostArgs(ctx, c, trig, logger))
	if err != nil {
		return err
	}
	checkArgs := guardCheck(trig.Pipeline, guards, pipelines.GuardContext{GitBranch: trig.GitBranch}, func() string {
		return checkoutDefaultBranch(ctx, trig.TriggerEnv[controller.EnvDefaultBranch], h.Dir)
	})
	masker := secrets.NewMasker()
	masker.Register(h.Token)
	env, err := podProcessEnv(masker, logger)
	if err != nil {
		return err
	}
	routes, err := nodehost.Start(logger)
	if err != nil {
		return err
	}
	defer func() { //nolint:contextcheck // safety: Close bounds its own shutdown and takes no context.
		if err := routes.Close(); err != nil {
			logger.Warn("trigger host: stop the node routes", "run_id", trig.ID, "err", err)
		}
	}()
	planSink, err := newPodPlanSink(ctx, trig.ID, masker, os.Stderr)
	if err != nil {
		return err
	}
	slots := NewHTTPConcurrency(h.ControllerURL, nil, h.Token, store.DefaultConcurrencyLease)
	dispatch := h.Runner
	if dispatch == nil {
		dispatch = &claimedNodeHost{controllerURL: h.ControllerURL, entry: h.Entry, dir: h.Dir, ctrl: c, logger: logger}
	}
	hostLog := &triggerHostLog{out: os.Stderr}
	shuttingDown := shuttingDownOn(ctx)
	rc, flags, err := triggerRunContext(ctx, c, trig)
	if err != nil {
		return err
	}
	rehydrate := hostedRehydration(Options{RetryOf: trig.RetryOf, Full: trig.Full})
	res, err := nodehost.Execute(ctx, nodehost.Config{
		Session:  nodehost.SessionConfig{Entry: h.Entry, Dir: h.Dir, Sink: planSink, Env: env, Engine: h.Engine, Runner: h.Identity, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementRunnerHosted, Secrets: masker}},
		Pipeline: trig.Pipeline, Args: args, CheckArgs: checkArgs,
		Run: rc, Requires: requires, Invocation: flags.Invocation(),
		StartAt: flags.StartAt, StopAt: flags.StopAt, Only: flags.Only,
		ParentRunID: trig.ParentRunID, RetryOf: trig.RetryOf, RetrySource: trig.RetrySource,
		State: retryPlanState{
			stoppingHostState: stoppingHostState{Client: c, shuttingDown: shuttingDown},
			retryOf:           trig.RetryOf, planHash: trig.TriggerEnv[retryprovenance.PlanHashKey],
		},
		Logger:    logger,
		Admission: &nodehost.Admission{Slots: slots},
		Memo:      &nodehost.Memo{Slots: slots, NoCache: flags.NoCache},
		// safety: the run host's eval processes read secrets through the trigger's claim, which the controller
		// releases them to, but get no forwarded routes, since only a node's claim backs them; their lines go to
		// this runner's log, since the node's log belongs to the host that runs it.
		Attempts: &nodehost.Attempts{
			Routes: routes, Resolver: podSecrets{c: client.NewWithToken(h.ControllerURL, nil, trig.ClaimToken), runID: trig.ID}, Masker: masker,
			OpenLog: func(context.Context, string, string) (nodehost.NodeLog, error) { return hostLog, nil },
		},
		Dispatch: dispatchThrough(dispatch, trig.Pipeline),
		// safety: a reclaimed trigger finds the rows its stopped host left, which the run keeps.
		Resume: true,
		// safety: a retry copies what its source run already finished; it is apart from Resume, which keeps this
		// run's own rows from an earlier host.
		Rehydrate: rehydrate,
	})
	if err != nil {
		return err
	}
	if shuttingDown() {
		// safety: the trigger stays claimed and its run unfinished, so its lease returns it to the queue.
		logger.Warn("trigger host: stopped before the run ended; its claim's lease returns it", "run_id", res.RunID)
		return nil
	}
	logger.Info("trigger host: run finished", "run_id", res.RunID, "status", res.Status, "err", res.Err)
	if err := c.FinishTrigger(context.WithoutCancel(ctx), trig.ID); err != nil {
		logger.Warn("trigger host: finish the trigger", "run_id", trig.ID, "err", err)
	}
	return nil
}

// safety: the checkout's sparkwing.yaml fills the arguments a run left out, its pipeline entry's over the project
// defaults, as a local run's do. Layering them again under a run row's arguments, which already hold them, changes
// nothing, so every planner of a run calls this and their plans agree.
func checkoutRunArgs(dir, pipeline string, args map[string]string) (map[string]string, pipelines.Guards, error) {
	_, cfg, err := projectconfig.Discover(dir)
	if err != nil {
		return nil, pipelines.Guards{}, &projectConfigError{err: err}
	}
	if cfg == nil {
		return args, pipelines.Guards{}, nil
	}
	opts := Options{Pipeline: pipeline, Args: args}
	if cfg.Defaults != nil {
		opts.DefaultArgs = cfg.Defaults.Args
	}
	opts.PipelineYAML = cfg.Registry().Find(pipeline)
	var rules pipelines.Guards
	if p := opts.PipelineYAML; p != nil {
		rules = p.Guards
	} else if cfg.Defaults != nil && cfg.Defaults.Guards != nil {
		rules = pipelines.Guards{Require: cfg.Defaults.Guards.When, Reject: cfg.Defaults.Guards.Unless}
	}
	return mergeInvokeArgs(opts), rules, nil
}

// safety: the host checks a run's guards once describe has filled in the SDK's defaults, against the arguments the
// run plans with, so leaving out a flag whose default a guard rejects does not slip past it. defaultBranch is asked
// only when a guard compares with the default branch, and a guard that does refuses the run when it is unknown,
// because an unresolved default would let a reject guard pass.
func guardCheck(pipeline string, rules pipelines.Guards, at pipelines.GuardContext, defaultBranch func() string) func(map[string]string) error {
	if rules.IsEmpty() {
		return nil
	}
	var needs string
	for _, raw := range append(slices.Clone(rules.Reject), rules.Require...) {
		if tok, err := pipelines.ParseGuardToken(raw); err == nil && tok.Kind == pipelines.KindGitBranchOnDef {
			needs = raw
		}
	}
	return func(args map[string]string) error {
		if needs != "" && at.GitDefaultBranch == "" {
			if at.GitDefaultBranch = defaultBranch(); at.GitDefaultBranch == "" {
				return fmt.Errorf("pipeline %q: guard %q could not resolve the repository's default branch, so the run is refused", pipeline, needs)
			}
		}
		at.Args = args
		return rules.Evaluate(pipeline, at)
	}
}

// hack: a variable so a test bounds the lookup below its real deadline.
var defaultBranchLookupTimeout = 15 * time.Second

// safety: a GitHub App trigger records the repository's default branch; any other run reads it from its checkout,
// origin's HEAD as a local run does, and asks origin when the clone did not record it. The ask runs with a source
// fetch's non-interactive environment and returns at its deadline even when a helper still holds git's output.
func checkoutDefaultBranch(ctx context.Context, recorded, dir string) string {
	if recorded != "" {
		return recorded
	}
	git := func(args ...string) string {
		ctx, cancel := context.WithTimeout(ctx, defaultBranchLookupTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env, cmd.WaitDelay = dir, bincache.NonInteractiveGitEnv(os.Environ()), time.Second
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if name := git("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); name != "" {
		return strings.TrimPrefix(name, "origin/")
	}
	for _, line := range strings.Split(git("ls-remote", "--symref", "origin", "HEAD"), "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			name, _, _ := strings.Cut(ref, "\t")
			return name
		}
	}
	return ""
}

// safety: a retry plans with the arguments its original run recorded.
func triggerHostArgs(ctx context.Context, c *client.Client, trig *store.Trigger, logger *slog.Logger) map[string]string {
	if trig.RetryOf == "" {
		return trig.Args
	}
	orig, err := c.GetRunForExecution(ctx, trig.RetryOf)
	if err != nil {
		logger.Warn("trigger host: the retried run's arguments are unreadable; planning with the trigger's",
			"retry_of", trig.RetryOf, "err", err)
		return trig.Args
	}
	return orig.Args
}

// safety: one derivation, [podRunContext], for the run host and every node host: the host applies it to the row it
// is about to record, the controller's run read (start time, the trigger's actor and pull request) with the trigger's
// git snapshot and source laid over it as the run row's upsert lays them, so each node host reads back that row.
//
// safety: a run row the controller does not have yet is planned from the trigger alone, its run flags included,
// as every pod then reads it; one it failed to answer for is retried and then refused, because planning without the row's pull request
// would hash differently from the pods that read it, and every node would fail with plan_drift.
func triggerRunContext(ctx context.Context, c *client.Client, trig *store.Trigger) (nodeproto.RunContext, runflags.Flags, error) {
	var row *store.Run
	var err error
	for attempt := 1; ; attempt++ {
		row, err = c.GetRunForExecution(ctx, trig.ID)
		if err == nil || errors.Is(err, store.ErrNotFound) || attempt == runReadAttempts {
			break
		}
		wait := time.NewTimer(time.Duration(attempt) * runReadBackoff)
		select {
		case <-ctx.Done():
			wait.Stop()
			return nodeproto.RunContext{}, runflags.Flags{}, fmt.Errorf("read run %s for its run context: %w", trig.ID, errors.Join(err, ctx.Err()))
		case <-wait.C:
		}
	}
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && row == nil):
		row = &store.Run{ID: trig.ID, TriggerUser: trig.TriggerUser, Invocation: runflags.FromTriggerEnv(trig.TriggerEnv).Invocation()}
	case err != nil:
		return nodeproto.RunContext{}, runflags.Flags{}, fmt.Errorf("read run %s for its run context: %w", trig.ID, err)
	}
	planned := *row
	planned.ID, planned.TriggerSource = trig.ID, trig.TriggerSource
	planned.GitSHA, planned.GitBranch = trig.GitSHA, trig.GitBranch
	planned.DeclaredRepo, planned.RepoURL = cmp.Or(row.DeclaredRepo, trig.Repo), cmp.Or(row.RepoURL, trig.RepoURL)
	if planned.StartedAt.IsZero() {
		planned.StartedAt = time.Now().UTC()
	}
	return podRunContext(&planned), runflags.FromInvocation(planned.Invocation), nil
}

const (
	runReadAttempts = 4
	runReadBackoff  = 250 * time.Millisecond
)

// HeartbeatTrigger renews triggerID's claim every interval until stop is
// called, and cancels the returned context when the claim ends: with a
// [nodehost.CancelError] for an operator's cancel, which the run records as
// cancelled, and with an error that fails the run when the claim ended any
// other way.
func HeartbeatTrigger(ctx context.Context, c *client.Client, triggerID string, interval time.Duration, logger *slog.Logger) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	var cancelled atomic.Bool
	beatCtx, stopBeat := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(beatCtx, c, triggerID, interval, func() {
			if cancelled.Load() {
				cancel(&nodehost.CancelError{Reason: "cancelled by operator"})
				return
			}
			cancel(errTriggerClaimEnded)
		}, &cancelled, logger)
	}()
	return ctx, func() { stopBeat(); <-done; cancel(nil) }
}

func dispatchThrough(r runner.Runner, pipeline string) nodehost.Executor {
	return func(ctx context.Context, n *nodehost.NodeRun) nodehost.Result {
		req := runner.Request{
			RunID: n.Run.ID, NodeID: n.Plan.ID, Pipeline: pipeline,
			ClaimWait: time.Duration(pointerValue(n.Run.Doc.ClaimWaitMS)) * time.Millisecond,
		}
		// safety: the node's retries and their backoff run on its host, so its Job outlives them all.
		req.Timeout = nodehost.AttemptsBudget(n.Plan)
		if resources := n.Plan.Resources; resources != nil {
			req.Cores, req.MemoryBytes = pointerValue(resources.Cores), pointerValue(resources.MemoryBytes)
		}
		res := r.RunNode(ctx, req)
		out := nodehost.Result{Outcome: res.Outcome, Err: res.Err}
		if data, ok := res.Output.([]byte); ok {
			out.Output = data
		}
		return out
	}
}

// safety: the node runs under a claim this runner takes for it, as a
// Kubernetes Job's pod does, so its rows, logs and routes are fenced to that
// claim and a second host of the same node is refused by the controller.
type claimedNodeHost struct {
	controllerURL, dir string
	entry              pipelineentry.Command
	ctrl               *client.Client
	logger             *slog.Logger
}

const claimedNodeHolder = "trigger-host:"

func (h *claimedNodeHost) RunNode(ctx context.Context, req runner.Request) runner.Result {
	n, err := h.ctrl.ClaimNodeByID(store.WithoutClaimFences(ctx), req.RunID, req.NodeID,
		claimedNodeHolder+req.RunID+"/"+req.NodeID, store.MaxLeaseDuration, false, nil)
	if err != nil {
		if ctx.Err() != nil {
			return runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
		}
		return runner.Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("claim node %s: %w", req.NodeID, err)}
	}
	fence := store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}
	ctx = store.WithNodeClaimFence(store.WithoutClaimFences(ctx), fence)
	ctx, abandon := context.WithCancelCause(ctx)
	defer abandon(nil)
	beatCtx, stopBeat := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		heartbeatDispatchedClaim(beatCtx, client.NewWithToken(h.controllerURL, nil, n.ClaimToken),
			req.RunID, req.NodeID, fence, store.MaxLeaseDuration, abandon, h.logger)
	}()
	defer func() { stopBeat(); wg.Wait() }()
	res, err := hostDispatchedNode(ctx, podNode{
		entry: h.entry, dir: h.dir, controllerURL: h.controllerURL, token: n.ClaimToken,
		runID: req.RunID, nodeID: req.NodeID, logger: h.logger,
	})
	if err == nil {
		return res
	}
	// safety: only this claim may write the node now, so a host that never started finishes the row under it.
	if ferr := client.NewWithToken(h.controllerURL, nil, n.ClaimToken).FinishNodeWithReason(context.WithoutCancel(ctx),
		req.RunID, req.NodeID, string(sparkwing.Failed), nodehost.RecordedErrText(err), nil, store.FailureUnknown, nil); ferr != nil {
		h.logger.Warn("trigger host: finish a node whose host never started", "run_id", req.RunID, "node_id", req.NodeID, "err", ferr)
	}
	return runner.Result{Outcome: sparkwing.Failed, Err: err}
}

// safety: a run host's eval and approval lines go to this runner's own log,
// one JSON record per line, because the node's log belongs to its node host.
type triggerHostLog struct {
	mu  sync.Mutex
	out io.Writer
}

func (l *triggerHostLog) Emit(rec sparkwing.LogRecord) {
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.out.Write(append(line, '\n')); err != nil {
		return
	}
}

func (l *triggerHostLog) Close() error { return nil }

// safety: a retry copies its source run's results only when it planned the source's plan.
type retryPlanState struct {
	stoppingHostState
	retryOf, planHash string
}

func (s retryPlanState) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	if err := retryPlanMatches(snapshot, runID, s.retryOf, s.planHash); err != nil {
		return err
	}
	return s.stoppingHostState.UpdatePlanSnapshot(ctx, runID, snapshot)
}
