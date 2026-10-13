package nodehost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// PlanDocument plans pipeline in a short-lived SDK process and returns the
// plan with the host's spec_hash on every node, validated as a hosted run's
// plan is, together with its stamped JSON.
func PlanDocument(ctx context.Context, cfg SessionConfig, pipeline string, args map[string]string, rc nodeproto.RunContext) (nodeproto.PlanDoc, []byte, error) {
	if cfg.Sink == nil {
		cfg.Sink = discardSink{}
	}
	if args == nil {
		args = map[string]string{}
	}
	sess, err := StartSession(ctx, cfg)
	if err != nil {
		return nodeproto.PlanDoc{}, nil, err
	}
	doc, raw, err := planRun(ctx, sess, pipeline, args, rc)
	_, cerr := sess.Close()
	var perr *nodeproto.Error
	if err == nil && errors.As(cerr, &perr) {
		// safety: a process that broke the protocol after its reply cannot vouch for that plan.
		return doc, nil, cerr
	}
	return doc, raw, err
}

// safety: the layers a pod hosts around its node, in the scheduler's order.
// The controller owns approvals and retry_auto, whose every attempt is another
// claim and so another pod; a plain retry re-runs in the pod, as the in-pod
// executor did. Admission runs only the node's concurrency group.
var (
	podLayers     = []Layer{skipIfLayer, admissionLayer, artifactsLayer, cachesLayer, retryLayer, timeoutLayer}
	podAutoLayers = []Layer{skipIfLayer, admissionLayer, artifactsLayer, cachesLayer, timeoutLayer}
)

// HostNode runs node nodeID of cfg's run, as a pod that claimed it does, and
// records the node's start and result in cfg.State. The node is planned
// afresh, and it runs only when its spec_hash equals acceptedHash, the one the
// run's accepted plan holds; otherwise it fails with plan_drift. attempt is
// the claim's 1-based attempt ordinal: a retry_auto node runs only that
// attempt, the last one when it exhausts the node's retry budget, capped at
// [store.MaxNodeRetries] as the controller caps it; any other node runs its
// plain retries in this process. cfg.Attempts is required.
func HostNode(ctx context.Context, cfg Config, nodeID, acceptedHash string, attempt int) Result {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	run := &Run{
		ID: cfg.Run.RunID, Pipeline: cfg.Pipeline, Args: cfg.Args, Context: cfg.Run,
		State: cfg.State, Logger: cfg.Logger,
	}
	res := podAttempt(ctx, &cfg, run, nodeID, acceptedHash, max(attempt, 1))
	if res.Outcome == sparkwing.Failed && ctx.Err() != nil {
		res.Outcome = sparkwing.Cancelled
	}
	if res.Err != nil && cfg.Attempts != nil {
		// safety: a plan failure quotes the SDK, so it is masked like an attempt's.
		res.Err = errors.New(cfg.Attempts.Masker.Mask(res.Err.Error()))
	}
	return podFinish(ctx, run, nodeID, res)
}

func podAttempt(ctx context.Context, cfg *Config, run *Run, nodeID, acceptedHash string, attempt int) Result {
	if cfg.Attempts == nil {
		return failed(errors.New("nodehost: no attempt executor"))
	}
	doc, _, err := PlanDocument(ctx, cfg.Session, cfg.Pipeline, cfg.Args, cfg.Run)
	if err != nil {
		return failed(fmt.Errorf("plan: %w", err))
	}
	run.Doc = doc
	i := slices.IndexFunc(doc.Nodes, func(n nodeproto.PlanNode) bool { return n.ID == nodeID })
	drift := func(format string, args ...any) Result {
		return failed(&nodeproto.Error{Code: nodeproto.CodePlanDrift, Message: fmt.Sprintf(format, args...)})
	}
	switch {
	case acceptedHash == "":
		return drift("the dispatcher handed node %s no accepted spec_hash", nodeID)
	case i < 0:
		return drift("node %s is not in this process's plan; plan code must be deterministic for a run", nodeID)
	case doc.Nodes[i].SpecHash != acceptedHash:
		return drift("node %s plans differently than when the run was accepted; plan code must be deterministic for a run", nodeID)
	}
	n := &NodeRun{Run: run, Plan: doc.Nodes[i]}
	layers := podLayers
	if m := n.Plan.Modifiers; m != nil && m.RetryAuto {
		layers, n.Attempt = podAutoLayers, attempt
		budget := min(max(m.Retry, 0), store.MaxNodeRetries)
		// safety: the controller's rule for claiming the node again, so after_run runs in the attempt it does not.
		n.Retried = func(r Result) bool {
			return r.Outcome == sparkwing.Failed && !r.Final && attempt <= budget && ctx.Err() == nil
		}
	}
	res := podChain(cfg, layers)(ctx, n)
	if len(res.expansions) > 0 && res.Outcome == sparkwing.Success {
		// safety: a pod cannot add nodes to its run, and dropping them would read as an empty fan-out.
		return failed(fmt.Errorf("node %s generates nodes, which a pod cannot host yet", nodeID))
	}
	return res
}

func podChain(cfg *Config, layers []Layer) Executor {
	inner := cfg.Attempts.executor(cfg)
	exec := func(ctx context.Context, n *NodeRun) Result {
		if err := n.Run.State.StartNode(ctx, n.Run.ID, n.Plan.ID); err != nil {
			return failed(fmt.Errorf("start node %s: %w", n.Plan.ID, err))
		}
		// safety: the dispatcher starts a node once per dispatch, and only retry_auto re-dispatches.
		if m := n.Plan.Modifiers; n.Attempt <= 1 || (m != nil && m.RetryAuto) {
			hostEvent(ctx, n.Run, n.Plan.ID, "node_started", nil)
		}
		return inner(ctx, n)
	}
	for i := len(layers) - 1; i >= 0; i-- {
		exec = layers[i](cfg, exec)
	}
	return exec
}

// safety: as the scheduler's finish, a refused output fails the node and a
// result the store never took never reads as a success.
func podFinish(ctx context.Context, run *Run, id string, res Result) Result {
	writeCtx := context.WithoutCancel(ctx)
	err := run.State.FinishNodeWithReason(writeCtx, run.ID, id, string(res.Outcome), errText(res.Err), res.Output, res.Reason, nil)
	if errors.Is(err, store.ErrOutputNotStored) {
		res = Result{Outcome: sparkwing.Failed, Err: err}
		err = run.State.FinishNodeWithReason(writeCtx, run.ID, id, string(res.Outcome), err.Error(), nil, store.FailureUnknown, nil)
	}
	if err != nil {
		run.Logger.Error("nodehost: record node result", "node", id, "err", err)
		res.Outcome, res.Err = sparkwing.Failed, errors.Join(res.Err, fmt.Errorf("record node result: %w", err))
	}
	nodeEndEvent(writeCtx, run, id, res)
	return res
}
