package warmpool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Config struct {
	PollInterval time.Duration

	ClaimWaitTimeout  time.Duration
	HeartbeatInterval time.Duration

	// UnmatchableGrace is how long a node whose labels this dispatcher's
	// fallback cannot advertise waits for a runner that can before it fails.
	// Zero means [DefaultUnmatchableGrace].
	UnmatchableGrace time.Duration
}

type Runner struct {
	ctrl           coordinator
	fallback       runner.Runner
	fallbackLabels []string
	cfg            Config
	logger         *slog.Logger
}

type coordinator interface {
	MarkNodeReady(context.Context, string, string) error
	AppendEvent(context.Context, string, string, string, []byte) error
	FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string,
		output []byte, reason string, exitCode *int) error
	UpdateNodeActivity(context.Context, string, string, string) error
	TouchNodeHeartbeat(context.Context, string, string) error
	GetNode(context.Context, string, string) (*store.Node, error)
	RevokeNodeReady(context.Context, string, string) (bool, error)
	FinalizeNodeReady(context.Context, string, string) (store.ExecutorClaimRoundResult, error)
}

func New(ctrl coordinator, fallback runner.Runner, cfg Config, logger *slog.Logger) *Runner {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.ClaimWaitTimeout <= 0 || cfg.ClaimWaitTimeout > 5*time.Second {
		cfg.ClaimWaitTimeout = 5 * time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = store.DispatchedHeartbeatInterval
	}
	if cfg.UnmatchableGrace <= 0 {
		cfg.UnmatchableGrace = DefaultUnmatchableGrace
	}
	if logger == nil {
		logger = slog.Default()
	}
	var fallbackLabels []string
	if advertised, ok := fallback.(runner.LabelAdvertiser); ok {
		fallbackLabels = advertised.AdvertisedLabels()
	}
	return &Runner{
		ctrl: ctrl, fallback: fallback, fallbackLabels: append([]string(nil), fallbackLabels...), cfg: cfg, logger: logger,
	}
}

var _ runner.Runner = (*Runner)(nil)

func (r *Runner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	if err := r.ctrl.MarkNodeReady(ctx, req.RunID, req.NodeID); err != nil {
		// safety: the server may have marked the node ready before the cancelled request failed
		if ctx.Err() != nil {
			return r.revokeAndReportCancelled(ctx, req)
		}
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
	var unmatchableSince time.Time

	for {
		select {
		case <-ctx.Done():
			return r.revokeAndReportCancelled(ctx, req)
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
			if n.Claimed {
				if !claimedSeen {
					stopHB()
					_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID, "claimed by remote executor")
				}
				claimedSeen = true
				continue
			}
			// safety: a labeled node may fall back only when the fallback explicitly
			// advertises every label. Most callers configure none.
			if !sparkwingruntime.MatchLabels(n.NeedsLabels, r.fallbackLabels) {
				now := time.Now()
				if unmatchableSince.IsZero() {
					unmatchableSince = now
				}
				if unmatchableExpired(unmatchableSince, now, r.cfg.UnmatchableGrace) {
					return r.failUnmatchable(ctx, req, n)
				}
				if time.Since(lastUnmatchableLog) >= unmatchableLogEvery {
					r.logger.Warn("warmpool: labeled node unclaimed",
						"run_id", req.RunID, "node_id", req.NodeID,
						"needs_labels", n.NeedsLabels,
						"fallback_labels", r.fallbackLabels,
						"cpu_class_cores", n.CreditCPUClassCores,
						"hint", "this dispatcher's fallback advertises none of these labels, so it waits "+
							"for a runner that does")
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
				return asCancellation(ctx, r.fallback.RunNode(ctx, req))
			}
		}
	}
}

// DefaultUnmatchableGrace is how long a node whose labels this dispatcher's
// fallback cannot advertise waits for a runner that can before it fails. It
// bounds the wait for a labeled runner that has yet to start, and is the only
// end a node above the warm cpu class has when nothing on the fleet serves it.
const DefaultUnmatchableGrace = 5 * time.Minute

// safety: the grace runs from the first sighting of an unmatchable node, and a
// wait exactly as long as the grace is still inside it.
func unmatchableExpired(since, now time.Time, grace time.Duration) bool {
	return now.Sub(since) > grace
}

// UnmatchableEvent is what a node records when no runner advertises its labels
// and no fallback may take it. It is the "node_unmatchable" event's payload.
type UnmatchableEvent struct {
	NeedsLabels    []string `json:"needs_labels"`
	FallbackLabels []string `json:"fallback_labels"`
	CPUClassCores  int64    `json:"cpu_class_cores,omitempty"`
	GraceSeconds   float64  `json:"grace_seconds"`
	Detail         string   `json:"detail"`
}

// safety: a node no runner advertises and no fallback may take would otherwise
// sit until the run's own deadline with nothing saying why, so the labels and
// the class it was billed at are what it fails with.
func (r *Runner) failUnmatchable(ctx context.Context, req runner.Request, n *store.Node) runner.Result {
	msg := fmt.Sprintf(
		"no runner advertises the labels %v within %s, and this dispatcher's fallback advertises %v",
		n.NeedsLabels, r.cfg.UnmatchableGrace, r.fallbackLabels)
	if n.CreditCPUClassCores > 0 {
		msg += fmt.Sprintf("; the node is billed at the %d-core class, which the warm pool does not serve",
			n.CreditCPUClassCores)
	}
	payload, err := json.Marshal(UnmatchableEvent{
		NeedsLabels: n.NeedsLabels, FallbackLabels: r.fallbackLabels,
		CPUClassCores: n.CreditCPUClassCores,
		GraceSeconds:  r.cfg.UnmatchableGrace.Seconds(), Detail: msg,
	})
	if err != nil {
		r.logger.Warn("warmpool: encoding the refusal failed",
			"run_id", req.RunID, "node_id", req.NodeID, "err", err)
		payload = []byte(`{}`)
	}
	if err := r.ctrl.AppendEvent(ctx, req.RunID, req.NodeID, "node_unmatchable", payload); err != nil {
		r.logger.Warn("warmpool: recording the refusal failed",
			"run_id", req.RunID, "node_id", req.NodeID, "err", err)
	}
	if err := r.ctrl.FinishNodeWithReason(ctx, req.RunID, req.NodeID,
		string(sparkwing.Failed), msg, nil, store.FailureQueueTimeout, nil); err != nil {
		r.logger.Warn("warmpool: failing the unmatchable node failed",
			"run_id", req.RunID, "node_id", req.NodeID, "err", err)
	}
	return runner.Result{Outcome: sparkwing.Failed, Err: errors.New(msg)}
}

func (r *Runner) revokeAndReportCancelled(ctx context.Context, req runner.Request) runner.Result {
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if _, err := r.ctrl.RevokeNodeReady(revokeCtx, req.RunID, req.NodeID); err != nil {
		r.logger.Debug("warmpool: cancellation revoke failed",
			"run_id", req.RunID, "node_id", req.NodeID, "err", err)
	}
	return runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
}

func asCancellation(ctx context.Context, res runner.Result) runner.Result {
	if ctx.Err() == nil || res.Outcome != sparkwing.Failed {
		return res
	}
	// safety: a fallback aborted by cancellation is a cancelled node, not a failed one
	if errors.Is(res.Err, context.Canceled) || errors.Is(res.Err, context.DeadlineExceeded) {
		return runner.Result{Outcome: sparkwing.Cancelled, Err: res.Err, Output: res.Output, Usage: res.Usage}
	}
	return res
}

func heartbeatLoop(
	ctx context.Context,
	ctrl coordinator,
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
