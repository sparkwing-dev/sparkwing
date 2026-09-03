package warmpool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Config struct {
	PollInterval time.Duration

	ClaimWaitTimeout  time.Duration
	HeartbeatInterval time.Duration
}

type Runner struct {
	ctrl     *client.Client
	fallback runner.Runner
	cfg      Config
	logger   *slog.Logger
}

func New(ctrl *client.Client, fallback runner.Runner, cfg Config, logger *slog.Logger) *Runner {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.ClaimWaitTimeout <= 0 || cfg.ClaimWaitTimeout > 5*time.Second {
		cfg.ClaimWaitTimeout = 5 * time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 5 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{ctrl: ctrl, fallback: fallback, cfg: cfg, logger: logger}
}

var _ runner.Runner = (*Runner)(nil)

func (r *Runner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	if err := r.ctrl.MarkNodeReady(ctx, req.RunID, req.NodeID); err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("mark ready: %w", err)}
	}
	_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID, "waiting for warm runner")

	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go heartbeatLoop(hbCtx, r.ctrl, req.RunID, req.NodeID, r.cfg.HeartbeatInterval, r.logger)

	poll := time.NewTicker(r.cfg.PollInterval)
	defer poll.Stop()

	claimedSeen := false
	waitDeadline := time.Now().Add(r.cfg.ClaimWaitTimeout)
	const unmatchableLogEvery = time.Minute
	var lastUnmatchableLog time.Time

	for {
		select {
		case <-ctx.Done():
			revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			if _, err := r.ctrl.RevokeNodeReady(revokeCtx, req.RunID, req.NodeID); err != nil {
				r.logger.Debug("warmpool: cancellation revoke failed",
					"run_id", req.RunID, "node_id", req.NodeID, "err", err)
			}
			cancel()
			return runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
		case <-poll.C:
			n, err := r.ctrl.GetNode(ctx, req.RunID, req.NodeID)
			if err != nil {
				r.logger.Warn("warmpool: GetNode failed",
					"run_id", req.RunID, "node_id", req.NodeID, "err", err)
				continue
			}
			if n.Status == "done" {
				return resultFromNode(n)
			}
			if n.ClaimedBy != "" {
				if !claimedSeen {
					stopHB()
					_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID,
						fmt.Sprintf("claimed by %s", n.ClaimedBy))
				}
				claimedSeen = true
				continue
			}
			// safety: labeled nodes must not fall back to K8sRunner; fallback Jobs don't advertise labels
			if len(n.NeedsLabels) > 0 {
				if time.Since(lastUnmatchableLog) >= unmatchableLogEvery {
					r.logger.Warn("warmpool: labeled node unclaimed",
						"run_id", req.RunID, "node_id", req.NodeID,
						"needs_labels", n.NeedsLabels,
						"hint", "no warm runner advertises these labels; start a runner with --label matching or remove .Requires()")
					lastUnmatchableLog = time.Now()
				}
				continue
			}
			if !claimedSeen && time.Now().After(waitDeadline) {
				resolution, rerr := r.ctrl.FinalizeNodeReady(ctx, req.RunID, req.NodeID)
				if rerr != nil {
					r.logger.Warn("warmpool: offer finalization failed",
						"run_id", req.RunID, "node_id", req.NodeID, "err", rerr)
					continue
				}
				if resolution.Pending {
					continue
				}
				if !resolution.Revoked {
					// safety: fallback must yield after the controller awards an offer
					stopHB()
					claimedSeen = true
					continue
				}
				if r.fallback == nil {
					return runner.Result{
						Outcome: sparkwing.Failed,
						Err:     errors.New("warmpool: no agent claimed and no fallback configured"),
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

func heartbeatLoop(
	ctx context.Context,
	ctrl *client.Client,
	runID, nodeID string,
	interval time.Duration,
	logger *slog.Logger,
) {
	_ = ctrl.TouchNodeHeartbeat(ctx, runID, nodeID)
	t := time.NewTicker(interval)
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

func resultFromNode(n *store.Node) runner.Result {
	oc := sparkwing.Outcome(n.Outcome)
	res := runner.Result{Outcome: oc}
	if n.Error != "" {
		res.Err = errors.New(n.Error)
	}
	if len(n.Output) > 0 {
		res.Output = n.Output
	}
	// safety: empty outcome means the agent wrote done without an outcome; treat as Failed
	if oc == "" {
		res.Outcome = sparkwing.Failed
		if res.Err == nil {
			res.Err = fmt.Errorf("node %s/%s done but outcome empty", n.RunID, n.NodeID)
		}
	}
	return res
}
