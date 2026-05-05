// Package warmpool is the Runner that hands node work to a pool of
// long-lived runner pods instead of spawning one K8s Job per node.
//
// Flow per node:
//
//  1. Orchestrator decides the node is ready (deps complete) and
//     calls RunNode on this Runner.
//  2. Runner calls MarkNodeReady on the controller; a warm runner
//     pod's claim loop will atomically flip claimed_by on the next
//     poll.
//  3. Runner polls GetNode until status='done' (pod finished writing
//     terminal state), or until a fallback deadline fires while the
//     node is still unclaimed, in which case the runner revokes
//     ready_at atomically and delegates to Fallback (typically a
//     K8sRunner creating a one-off Job). The revoke guarantees the
//     pod and Fallback never race -- whichever takes it first owns
//     the execution.
//
// Latency win: a claim-plus-exec cycle on a warm pod is ~100ms once
// the image is pulled, versus 5-15s for a fresh Job (pod schedule +
// image pull + binary start). For short pipelines with many small
// nodes, that's the difference between noticeable and imperceptible
// iteration.
package warmpool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Config tunes the Runner's claim-wait + poll behavior.
type Config struct {
	// PollInterval is how often GetNode is called while waiting on
	// the pod to finish. 500ms feels instant to a human without
	// hammering the controller.
	PollInterval time.Duration

	// ClaimWaitTimeout is how long to wait for SOME pod to claim the
	// node before falling back to the K8sRunner path. A node that
	// sits unclaimed past this threshold indicates an empty or
	// unreachable pool. 5s is a comfortable default in a cluster with
	// 3 warm replicas and sub-second claim polls; tune up for deeper
	// queues.
	ClaimWaitTimeout time.Duration
}

// Runner is a runner.Runner that dispatches through the warm pool.
type Runner struct {
	ctrl     *client.Client
	fallback runner.Runner
	cfg      Config
	logger   *slog.Logger
}

// New builds a Runner that marks nodes ready on ctrl and falls back
// to `fallback` (typically a K8sRunner) when no pod claims within
// cfg.ClaimWaitTimeout. Passing a nil fallback disables the fallback;
// RunNode will block until a pod eventually claims the node or ctx
// cancels.
func New(ctrl *client.Client, fallback runner.Runner, cfg Config, logger *slog.Logger) *Runner {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.ClaimWaitTimeout <= 0 {
		cfg.ClaimWaitTimeout = 5 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{ctrl: ctrl, fallback: fallback, cfg: cfg, logger: logger}
}

var _ runner.Runner = (*Runner)(nil)

// RunNode releases the node to the pool and waits for its terminal
// state. Session 3's scope: the orchestrator remains the authority
// for "when is a node ready"; the pool is the execution layer.
func (r *Runner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	if err := r.ctrl.MarkNodeReady(ctx, req.RunID, req.NodeID); err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("mark ready: %w", err)}
	}
	// Stamp pre-claim activity once; heartbeats from here on keep the
	// dashboard's liveness dot green while we wait for a pool pod.
	// Activity transitions when a claim is first observed.
	_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID, "waiting for warm runner")

	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go heartbeatLoop(hbCtx, r.ctrl, req.RunID, req.NodeID, r.logger)

	poll := time.NewTicker(r.cfg.PollInterval)
	defer poll.Stop()

	claimedSeen := false
	waitDeadline := time.Now().Add(r.cfg.ClaimWaitTimeout)
	// Throttle the "no matching runner" warning. Labeled nodes block
	// indefinitely waiting for a matching runner; we log once per
	// minute so a misconfigured RunsOn is visible without spamming.
	const unmatchableLogEvery = time.Minute
	var lastUnmatchableLog time.Time

	for {
		select {
		case <-ctx.Done():
			return runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
		case <-poll.C:
			n, err := r.ctrl.GetNode(ctx, req.RunID, req.NodeID)
			if err != nil {
				// Transient controller hiccup. Keep polling; if the
				// failure persists the outer run ctx will eventually
				// cancel us.
				r.logger.Warn("warmpool: GetNode failed",
					"run_id", req.RunID, "node_id", req.NodeID, "err", err)
				continue
			}
			if n.Status == "done" {
				return resultFromNode(n)
			}
			if n.ClaimedBy != "" {
				if !claimedSeen {
					// First observation of a claim; note the holder so
					// operators can correlate with a specific warm runner.
					_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID,
						fmt.Sprintf("claimed by %s", n.ClaimedBy))
				}
				claimedSeen = true
				continue
			}
			// Labeled nodes (RunsOn) skip the K8sRunner fallback:
			// fallback Jobs don't advertise labels, so handing them a
			// labeled node defeats the point. Block on the warm pool
			// until a matching runner connects; periodically warn so
			// an unmatchable RunsOn is visible to the operator.
			if len(n.NeedsLabels) > 0 {
				if time.Since(lastUnmatchableLog) >= unmatchableLogEvery {
					r.logger.Warn("warmpool: labeled node unclaimed",
						"run_id", req.RunID, "node_id", req.NodeID,
						"needs_labels", n.NeedsLabels,
						"hint", "no warm runner advertises these labels; start a runner with --label matching or remove .RunsOn()")
					lastUnmatchableLog = time.Now()
				}
				continue
			}
			if !claimedSeen && time.Now().After(waitDeadline) {
				// Pool was empty or unreachable for long enough to
				// warrant fallback. Revoke atomically so we don't
				// double-dispatch if a pod claims in the next tick.
				revoked, rerr := r.ctrl.RevokeNodeReady(ctx, req.RunID, req.NodeID)
				if rerr != nil {
					r.logger.Warn("warmpool: revoke failed",
						"run_id", req.RunID, "node_id", req.NodeID, "err", rerr)
					continue
				}
				if !revoked {
					// A pod claimed between our GetNode and
					// RevokeNodeReady. Race lost; stay in the poll
					// loop and let the pod finish the work.
					claimedSeen = true
					continue
				}
				if r.fallback == nil {
					return runner.Result{
						Outcome: sparkwing.Failed,
						Err:     errors.New("warmpool: no pod claimed and no fallback configured"),
					}
				}
				r.logger.Warn("warmpool: no claim in window; falling back",
					"run_id", req.RunID, "node_id", req.NodeID,
					"wait", r.cfg.ClaimWaitTimeout)
				return r.fallback.RunNode(ctx, req)
			}
		}
	}
}

// heartbeatLoop stamps last_heartbeat on (runID, nodeID) every 5s
// until ctx cancels. Errors are logged but not surfaced: a missed
// heartbeat is a UI annoyance, not a correctness issue.
func heartbeatLoop(ctx context.Context, ctrl *client.Client, runID, nodeID string, logger *slog.Logger) {
	_ = ctrl.TouchNodeHeartbeat(ctx, runID, nodeID)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := ctrl.TouchNodeHeartbeat(ctx, runID, nodeID); err != nil {
				logger.Debug("warmpool: heartbeat failed",
					"run_id", runID, "node_id", nodeID, "err", err)
			}
		}
	}
}

// resultFromNode maps a terminal node row to a runner.Result. Same
// shape as K8sRunner.readFinalResult; kept local rather than
// exported because both callers want slightly different defensive
// checks.
func resultFromNode(n *store.Node) runner.Result {
	oc := sparkwing.Outcome(n.Outcome)
	res := runner.Result{Outcome: oc}
	if n.Error != "" {
		res.Err = errors.New(n.Error)
	}
	if len(n.Output) > 0 {
		res.Output = []byte(n.Output)
	}
	// Defensive: if the pod wrote terminal state without an outcome,
	// treat as Failed so the orchestrator sees something deterministic
	// rather than an empty string.
	if oc == "" {
		res.Outcome = sparkwing.Failed
		if res.Err == nil {
			res.Err = fmt.Errorf("node %s/%s done but outcome empty", n.RunID, n.NodeID)
		}
	}
	return res
}
