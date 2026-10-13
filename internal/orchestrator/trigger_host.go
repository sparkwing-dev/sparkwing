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
	// Heartbeat, when positive, renews the trigger claim and cancels the run
	// when the claim ends or an operator cancels it. Zero leaves the claim to
	// the caller, which cancels ctx with a [nodehost.CancelError] for an
	// operator's cancel.
	Heartbeat time.Duration
	Logger    *slog.Logger
}

var errTriggerClaimEnded = errors.New("the trigger claim ended, so the run stopped")

// HostTriggerRun plans h's run in a short-lived process of its pipeline,
// runs the scheduler, dispatches each node, and finishes the trigger once
// the run row is terminal. An error means no run row was recorded.
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
	if h.Heartbeat > 0 {
		var stop func()
		ctx, stop = heartbeatTrigger(ctx, c, trig.ID, h.Heartbeat, logger)
		defer stop()
	}
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
	res, err := nodehost.Execute(ctx, nodehost.Config{
		Session:  nodehost.SessionConfig{Entry: h.Binary, Dir: h.Dir, Sink: planSink},
		Pipeline: trig.Pipeline, Args: triggerHostArgs(ctx, c, trig, logger),
		Run: triggerRunContext(ctx, c, trig), Requires: requires,
		ParentRunID: trig.ParentRunID, RetryOf: trig.RetryOf, RetrySource: trig.RetrySource,
		State: c, Logger: logger,
		Admission: &nodehost.Admission{Slots: slots},
		Memo: &nodehost.Memo{Slots: slots, Output: func(ctx context.Context, _ store.ClaimInputRequest, runID, nodeID string) ([]byte, error) {
			return c.GetNodeOutput(ctx, runID, nodeID)
		}},
		// safety: the run host's eval processes get no forwarded routes, since only a node's claim backs them,
		// and their lines go to this runner's log, since the node's log belongs to the host that runs it.
		Attempts: &nodehost.Attempts{
			Routes: routes, Resolver: podSecrets{c: c, runID: trig.ID}, Masker: masker,
			OpenLog: func(context.Context, string, string) (nodehost.NodeLog, error) { return hostLog, nil },
		},
		Dispatch: dispatchThrough(dispatch, trig.Pipeline),
	})
	planSink.end()
	if err != nil {
		return err
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

// safety: the context is the one [podRunContext] derives from the run row
// this host records, so each dispatched node's plan hashes alike: the start
// time the row holds, the trigger's git snapshot and its source, and nothing
// the row does not carry.
func triggerRunContext(ctx context.Context, c *client.Client, trig *store.Trigger) nodeproto.RunContext {
	started := time.Now().UTC()
	if row, err := c.GetRunForExecution(ctx, trig.ID); err == nil && !row.StartedAt.IsZero() {
		started = row.StartedAt
	}
	return nodeproto.RunContext{
		RunID: trig.ID, StartedAt: started,
		Git:     &nodeproto.Git{SHA: trig.GitSHA, Branch: trig.GitBranch, Repo: trig.Repo, RepoURL: trig.RepoURL},
		Trigger: &nodeproto.Trigger{Source: cmp.Or(trig.TriggerSource, "manual")},
	}
}

// safety: the claim's cancel is a cancel, which the run records as
// cancelled; a claim that ended any other way fails the run.
func heartbeatTrigger(ctx context.Context, c *client.Client, triggerID string, interval time.Duration, logger *slog.Logger) (context.Context, func()) {
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
		if m := n.Plan.Modifiers; m != nil {
			req.Timeout = time.Duration(m.TimeoutMS) * time.Millisecond
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
			runID: req.RunID, nodeID: req.NodeID, artifacts: art, logger: h.logger,
		}); err == nil {
			return res
		}
	}
	// safety: only this claim may write the node now, so a host that never started finishes the row under it.
	if ferr := client.NewWithToken(h.controllerURL, nil, n.ClaimToken).FinishNodeWithReason(context.WithoutCancel(ctx),
		req.RunID, req.NodeID, string(sparkwing.Failed), err.Error(), nil, store.FailureUnknown, nil); ferr != nil {
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
