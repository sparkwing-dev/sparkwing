package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	goruntime "runtime"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type nodeSpawnHandler struct {
	runner   *NodeExecutor
	backends Backends
	plan     *sparkwing.Plan
	runID    string
	pipeline string

	parentNodeID     string
	delegate         sparkwing.Logger
	pipelineRequires []string

	slots chan struct{}
}

func newNodeSpawnHandler(
	r *NodeExecutor,
	backends Backends,
	plan *sparkwing.Plan,
	runID, pipeline, parentNodeID string,
	delegate sparkwing.Logger,
	pipelineRequires []string,
) *nodeSpawnHandler {
	return &nodeSpawnHandler{
		runner:           r,
		backends:         backends,
		plan:             plan,
		runID:            runID,
		pipeline:         pipeline,
		parentNodeID:     parentNodeID,
		delegate:         delegate,
		pipelineRequires: pipelineRequires,
		slots:            make(chan struct{}, goruntime.NumCPU()),
	}
}

var _ sparkwing.SpawnHandler = (*nodeSpawnHandler)(nil)

func (h *nodeSpawnHandler) Spawn(ctx context.Context, parentNodeID, spawnID string, job sparkwing.Workable) (any, error) {
	if h == nil || h.runner == nil {
		return nil, fmt.Errorf("orchestrator: spawn handler not bound to a node runner")
	}
	if parentNodeID == "" {
		parentNodeID = h.parentNodeID
	}

	child, err := admitSpawnChild(spawnAdmission{
		writeCtx:         context.WithoutCancel(ctx),
		plan:             h.plan,
		state:            h.backends.State,
		runID:            h.runID,
		pipelineRequires: h.pipelineRequires,
	}, parentNodeID, spawnID, job)
	if err != nil {
		return nil, err
	}
	childID := child.ID()

	if reason, mismatch := h.runnerMismatch(ctx, child); mismatch {
		h.runner.markSkipped(ctx, h.runID, childID, reason)
		return nil, nil
	}

	// safety: the parent is not idle while its child runs, it is blocked
	// on it, so its no-progress budget must not tick down here --
	// exactly as on the dispatcher's path.
	resumeProgressTimeout := pauseProgressTimeout(ctx)
	defer resumeProgressTimeout()

	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	case <-ctx.Done():
		h.markChildCancelled(ctx, childID)
		return nil, spawnCancelledError(childID, ctx.Err())
	}

	res := h.runner.RunNode(ctx, runner.Request{
		RunID:    h.runID,
		NodeID:   childID,
		Pipeline: h.pipeline,
		Node:     child,
		Delegate: h.delegate,
	})

	if res.Outcome == sparkwing.Failed && ctx.Err() != nil && canceledByRun(res.Err) {
		h.markChildCancelled(ctx, childID)
		return nil, spawnCancelledError(childID, ctx.Err())
	}
	if !res.Outcome.OK() {
		msg := ""
		if res.Err != nil {
			msg = res.Err.Error()
		}
		return nil, spawnFailedError(childID, res.Outcome, msg)
	}
	return res.Output, nil
}

func (h *nodeSpawnHandler) runnerMismatch(ctx context.Context, child *sparkwing.JobNode) (string, bool) {
	labels := child.WhenRunnerLabels()
	if len(labels) == 0 {
		return "", false
	}
	advertised := h.runner.AdvertisedLabels()
	if info := sparkwing.Runner(ctx); info != nil && len(info.Labels) > 0 {
		advertised = info.Labels
	}
	if sparkwingruntime.MatchLabels(labels, advertised) {
		return "", false
	}
	return fmt.Sprintf("WhenRunner labels %v not satisfied by active runner %v",
		labels, advertised), true
}

func (h *nodeSpawnHandler) markChildCancelled(ctx context.Context, childID string) {
	const reason = "cancelled: run failing"
	writeCtx := context.WithoutCancel(ctx)
	_ = h.backends.State.FinishNode(writeCtx, h.runID, childID, string(sparkwing.Cancelled), reason, nil)
	_ = h.backends.State.AppendEvent(writeCtx, h.runID, childID, "node_cancelled", []byte(reason))
}

func nodeProcessPipelineRequires(pipeline string, logger *slog.Logger) []string {
	cfg := checkoutProjectConfig(logger)
	if cfg == nil {
		return nil
	}
	entry := (&pipelines.Config{Pipelines: cfg.Pipelines}).Find(pipeline)
	if entry == nil {
		return nil
	}
	return entry.Requires
}
