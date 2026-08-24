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

// nodeSpawnHandler fires SpawnNode from inside a node's own process --
// a pod, or since process-per-node any local node.
//
// The dispatcher's handler cannot serve these call sites: it splices
// the child into the live plan object and hands it to the dispatch
// loop, and neither exists here. Until this handler did, a node
// running outside the dispatcher had no handler at all, so RunWork
// refused any Work declaring a spawn -- spawn was a feature that
// worked only while the node happened to share the dispatcher's
// process.
//
// The child runs in this process, under the parent's context. That is
// what a spawn already is: dynamic sub-work the parent discovered and
// waits on, whose CPU and memory belong to the parent's node however
// the run is deployed. Sending it back to the dispatcher to be
// scheduled would need a plan the dispatcher never planned, and in a
// pod there is nothing on the other end to send it to.
type nodeSpawnHandler struct {
	runner   *InProcessRunner
	backends Backends
	plan     *sparkwing.Plan
	runID    string
	pipeline string
	// parentNodeID is the node this process is executing, used when the
	// call site carries no node in its context.
	parentNodeID     string
	delegate         sparkwing.Logger
	pipelineRequires []string

	// slots bounds how many spawn children run at once, restoring the
	// MaxParallel cap the dispatcher applies to every node it schedules
	// (runtime.NumCPU on a local run). Without it one JobSpawnEach over
	// twenty items ran twenty node bodies at once inside a single
	// admission lease.
	//
	// It is this process's bound, not the run's: N node processes each
	// admit up to NumCPU children. Closing that gap means re-admitting
	// every child against the host arbiter, which is its own ticket. A
	// spawn chain still pins one slot per layer, exactly as the
	// dispatcher's worker slots do.
	slots chan struct{}
}

// newNodeSpawnHandler binds a handler to the node this process runs.
func newNodeSpawnHandler(
	r *InProcessRunner,
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

// Spawn creates the child's node row, runs its body here, and returns
// its output to the suspended parent step.
//
// It enters through the runner's RunNode rather than calling the body
// directly, which is what gives the child the execution path every
// dispatched node gets: its own node log with the annotation, summary,
// step-state and masker wrappers, its start and terminal row writes and
// events, its metrics sampler and heartbeat, and the unfinished-row
// safety net for a failure that happens before any of those are
// written.
//
// The plan-time modifiers RunNode also resolves -- Cache, Concurrency,
// SkipIf -- are unset here and cannot be otherwise: a spawn child is
// built by NewDetachedNode out of a Workable, and nothing on that path
// can call a Plan-layer modifier. The label providers are the exception
// (a Workable can declare WhenRunner), and they are gated below.
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

// runnerMismatch reports whether this process fails the child's
// WhenRunner terms, carrying the dispatcher's wording for the row.
//
// The dispatcher gates WhenRunner in its dispatch loop, above the
// runner, so a child spawned inside a node process met no gate at all:
// a WhenRunner("gpu") child ran on whatever box its parent landed on
// and recorded a green row for it. What this process advertises is the
// RunnerInfo the run stamped on it -- SPARKWING_RUNNER_LABELS in a
// spawned node, the trigger loop's env in a pod -- falling back to the
// runner's own advertisement when nothing installed one.
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

// markChildCancelled records a child torn down with its parent as
// cancelled rather than failed, the way the dispatcher does for a node
// it scheduled. The execution path writes nothing when the failure is
// the cancellation itself, so without this the child would be left
// mid-flight in the run record forever.
//
// Unconditional, like the dispatcher's markRunCancelled: FinishNode
// only writes a row that has no outcome yet, and the event has to go
// out either way or a reader sees a cancelled row nothing announced.
func (h *nodeSpawnHandler) markChildCancelled(ctx context.Context, childID string) {
	const reason = "cancelled: run failing"
	writeCtx := context.WithoutCancel(ctx)
	_ = h.backends.State.FinishNode(writeCtx, h.runID, childID, string(sparkwing.Cancelled), reason, nil)
	_ = h.backends.State.AppendEvent(writeCtx, h.runID, childID, "node_cancelled", []byte(reason))
}

// nodeProcessPipelineRequires reads the pipeline-level runner labels a
// spawn child's row carries, from the same yaml entry the dispatcher
// read them from when it created the plan's rows.
//
// The plan snapshot does not carry them, so a node process has to go
// back to the project config in its checkout. Missing config yields no
// labels, which is what a pipeline declaring none produces anyway.
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
