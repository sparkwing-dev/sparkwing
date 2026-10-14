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
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
	// Binary is the pipeline's built entry, and Dir the repository root it
	// runs in.
	Binary, Dir string
	// Runner dispatches each node, as a Kubernetes Job or to a warm runner.
	// Nil hosts each node in this process under a claim of its own.
	Runner runner.Runner
	Logger *slog.Logger
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
	masker := secrets.NewMasker()
	masker.Register(h.Token)
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
		dispatch = &claimedNodeHost{controllerURL: h.ControllerURL, binary: h.Binary, dir: h.Dir, ctrl: c, logger: logger}
	}
	hostLog := &triggerHostLog{out: os.Stderr}
	shuttingDown := shuttingDownOn(ctx)
	rc, err := triggerRunContext(ctx, c, trig)
	if err != nil {
		return err
	}
	rehydrate, err := hostedRehydration(ctx, c, Options{RetryOf: trig.RetryOf, RetrySource: trig.RetrySource, Full: trig.Full}, trig.ID)
	if err != nil {
		return err
	}
	res, err := nodehost.Execute(ctx, nodehost.Config{
		Session:  nodehost.SessionConfig{Entry: h.Binary, Dir: h.Dir, Sink: planSink},
		Pipeline: trig.Pipeline, Args: triggerHostArgs(ctx, c, trig, logger),
		Run: rc, Requires: requires,
		ParentRunID: trig.ParentRunID, RetryOf: trig.RetryOf, RetrySource: trig.RetrySource,
		State: retryPlanState{
			stoppingHostState: stoppingHostState{Client: c, shuttingDown: shuttingDown},
			retryOf:           trig.RetryOf, planHash: trig.TriggerEnv[retryprovenance.PlanHashKey],
		},
		Logger:    logger,
		Admission: &nodehost.Admission{Slots: slots},
		Memo: &nodehost.Memo{Slots: slots, Source: func(ctx context.Context, _ store.ClaimInputRequest, runID, nodeID string) (nodehost.MemoSource, error) {
			return nodehost.ReadMemoSource(ctx, c, runID, nodeID)
		}},
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
	planSink.end()
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
// safety: a run row the controller does not have yet is planned from the trigger alone, as every pod then reads
// it; one it failed to answer for is retried and then refused, because planning without the row's pull request
// would hash differently from the pods that read it, and every node would fail with plan_drift.
func triggerRunContext(ctx context.Context, c *client.Client, trig *store.Trigger) (nodeproto.RunContext, error) {
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
			return nodeproto.RunContext{}, fmt.Errorf("read run %s for its run context: %w", trig.ID, errors.Join(err, ctx.Err()))
		case <-wait.C:
		}
	}
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && row == nil):
		row = &store.Run{ID: trig.ID, TriggerUser: trig.TriggerUser}
	case err != nil:
		return nodeproto.RunContext{}, fmt.Errorf("read run %s for its run context: %w", trig.ID, err)
	}
	planned := *row
	planned.ID, planned.TriggerSource = trig.ID, trig.TriggerSource
	planned.GitSHA, planned.GitBranch = trig.GitSHA, trig.GitBranch
	planned.DeclaredRepo, planned.RepoURL = cmp.Or(row.DeclaredRepo, trig.Repo), cmp.Or(row.RepoURL, trig.RepoURL)
	if planned.StartedAt.IsZero() {
		planned.StartedAt = time.Now().UTC()
	}
	return podRunContext(&planned), nil
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
			ClaimWait: time.Duration(n.Run.Doc.ClaimWaitMS) * time.Millisecond,
		}
		// safety: the node's retries and their backoff run on its host, so its Job outlives them all.
		req.Timeout = nodehost.AttemptsBudget(n.Plan)
		if m := n.Plan.Modifiers; m != nil {
			req.Cores, req.MemoryBytes = m.ResCores, m.ResMemoryBytes
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
	controllerURL, binary, dir string
	ctrl                       *client.Client
	logger                     *slog.Logger
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
	grant := RequestRunCacheGrant(ctx, h.controllerURL, n.ClaimToken, req.RunID, h.logger)
	art, err := supervisorArtifactStore(ctx, h.controllerURL, req.RunID, "", grant)
	if err == nil {
		var res runner.Result
		if res, err = hostDispatchedNode(ctx, podNode{
			binary: h.binary, dir: h.dir, controllerURL: h.controllerURL, token: n.ClaimToken,
			runID: req.RunID, nodeID: req.NodeID, cacheGrant: grant, artifacts: art, logger: h.logger,
		}); err == nil {
			return res
		}
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
