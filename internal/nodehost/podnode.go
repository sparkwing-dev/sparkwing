package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// PlanRequest is what [PlanDocument] asks a pipeline process to plan.
type PlanRequest struct {
	Pipeline string
	// Args are the run's flag values; describe's defaults fill in the rest.
	Args map[string]string
	Run  nodeproto.RunContext
	// Requires are the placement labels the host reads from the pipeline's
	// YAML; they join the plan's own.
	Requires []string
	// Masker, when set, has the value of every flag describe marks secret
	// registered before plan code runs.
	Masker *secrets.Masker
	// StartAt and StopAt must name steps of the plan, and Only cuts every
	// node it does not keep from the plan, for a run no host schedules.
	StartAt, StopAt, Only string
}

// PlannedRun is a plan [PlanDocument] accepted: the document with the host's
// spec_hash on every node, its JSON, and the flag values it was planned with,
// which every later process of the run must plan with too.
type PlannedRun struct {
	Doc  nodeproto.PlanDoc
	JSON []byte
	Args map[string]string
}

// PlanDocument plans a pipeline in a short-lived SDK process, as a hosted
// run's plan process does, and validates the plan the same way.
func PlanDocument(ctx context.Context, cfg SessionConfig, req PlanRequest) (PlannedRun, error) {
	if cfg.Sink == nil {
		cfg.Sink = discardSink{}
	}
	sess, err := StartSession(ctx, dryRunSession(cfg, req.Run.DryRun))
	if err != nil {
		return PlannedRun{}, err
	}
	var out PlannedRun
	out.Args, err = describeArgs(ctx, sess, req.Pipeline, req.Args, req.Masker)
	if err == nil {
		out.Doc, out.JSON, err = planRun(ctx, sess, req.Pipeline, out.Args, req.Run)
	}
	if err == nil {
		out.Doc, out.JSON, err = hostRequires(out.Doc, out.JSON, req.Requires)
	}
	if err == nil {
		err = validateStepRange(out.Doc, req.StartAt, req.StopAt)
	}
	if err == nil && req.Only != "" {
		if req.StartAt != "" || req.StopAt != "" {
			err = fmt.Errorf("--only is mutually exclusive with --start-at / --stop-at")
		} else {
			out.Doc, out.JSON, err = cutOutsideOnly(out.Doc, out.JSON, req.Only)
		}
	}
	_, cerr := sess.Close()
	var perr *nodeproto.Error
	if err == nil && errors.As(cerr, &perr) {
		// safety: a process that broke the protocol after its reply cannot vouch for that plan.
		return PlannedRun{}, cerr
	}
	return out, err
}

// safety: placement labels from the pipeline's YAML are the host's, so plan
// code can add labels but never drop them.
func hostRequires(doc nodeproto.PlanDoc, raw []byte, requires []string) (nodeproto.PlanDoc, []byte, error) {
	if len(requires) == 0 {
		return doc, raw, nil
	}
	doc.Requires = appendNew(slices.Clone(doc.Requires), requires)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return doc, nil, err
	}
	labels, err := json.Marshal(doc.Requires)
	if err != nil {
		return doc, nil, err
	}
	fields["requires"] = labels
	raw, err = json.Marshal(fields)
	return doc, raw, err
}

// safety: a pod's layers in scheduler order. The controller owns approvals and retry_auto, whose every
// attempt is another claim and pod; a plain retry re-runs in the pod as the in-pod executor did. Memo
// reads and records through the claim, and admission runs only the node's concurrency group.
var (
	podLayers     = []Layer{skipIfLayer, memoLayer, admissionLayer, artifactsLayer, cachesLayer, retryLayer, timeoutLayer}
	podAutoLayers = []Layer{skipIfLayer, memoLayer, admissionLayer, artifactsLayer, cachesLayer, timeoutLayer}
	// safety: a dispatched node's run host already ran its skip_if, memo and
	// admission, so the pod runs the rest, retry_auto's attempts included.
	podDispatchedLayers = []Layer{artifactsLayer, cachesLayer, retryLayer, timeoutLayer}
)

// HostNode runs node nodeID of cfg's run, as a pod that claimed it does, and
// records the node's start and result in cfg.State. The node is planned
// afresh, and it runs only when its spec_hash equals acceptedHash, the one the
// run's accepted plan holds; otherwise it fails with plan_drift. attempt is
// the claim's 1-based attempt ordinal: a retry_auto node runs only that
// attempt, with before_run as every dispatch did, and after_run only when it
// is the last, the one that exhausts the node's retry budget, capped at
// [store.MaxNodeRetries] as the controller caps it; any other node runs its
// plain retries in this process. cfg.Attempts is required.
func HostNode(ctx context.Context, cfg Config, nodeID, acceptedHash string, attempt int) Result {
	run := podRun(&cfg)
	return podHost(ctx, &cfg, run, nodeID, podAttempt(ctx, &cfg, run, nodeID, acceptedHash, max(attempt, 1), false))
}

// HostDispatchedNode runs node nodeID of cfg's run for the run host that
// dispatched it, which already ran the node's skip_if, memo and admission;
// it records the node's start and result in cfg.State as [HostNode] does. The
// node runs only when its spec_hash equals acceptedHash, and every retry, a
// retry_auto node's included, runs in this process. cfg.Attempts is required.
func HostDispatchedNode(ctx context.Context, cfg Config, nodeID, acceptedHash string) Result {
	run := podRun(&cfg)
	return podHost(ctx, &cfg, run, nodeID, podAttempt(ctx, &cfg, run, nodeID, acceptedHash, 1, true))
}

func podRun(cfg *Config) *Run {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Run{
		ID: cfg.Run.RunID, Pipeline: cfg.Pipeline, Args: cfg.Args, Context: cfg.Run,
		StartAt: cfg.StartAt, StopAt: cfg.StopAt, State: cfg.State, Logger: cfg.Logger,
	}
}

func podHost(ctx context.Context, cfg *Config, run *Run, nodeID string, res Result) Result {
	if res.Outcome == sparkwing.Failed && ctx.Err() != nil {
		res.Outcome = sparkwing.Cancelled
	}
	if res.Err != nil && cfg.Attempts != nil {
		// safety: a plan failure quotes the SDK, so it is masked like an attempt's.
		res.Err = errors.New(cfg.Attempts.Masker.Mask(res.Err.Error()))
	}
	return podFinish(ctx, run, nodeID, res)
}

func podAttempt(ctx context.Context, cfg *Config, run *Run, nodeID, acceptedHash string, attempt int, dispatched bool) Result {
	if cfg.Attempts == nil {
		return failed(errors.New("nodehost: no attempt executor"))
	}
	planned, err := PlanDocument(ctx, cfg.Session, PlanRequest{
		Pipeline: cfg.Pipeline, Args: cfg.Args, Run: cfg.Run, Requires: cfg.Requires, Masker: cfg.Attempts.Masker,
	})
	if err != nil {
		return failed(fmt.Errorf("plan: %w", err))
	}
	doc := planned.Doc
	run.Doc, run.Args = doc, planned.Args
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
	if m := n.Plan.Modifiers; dispatched {
		layers = podDispatchedLayers
		if parent := n.Plan.OnFailureOf; parent != "" {
			row, err := run.State.GetNode(ctx, run.ID, parent)
			if err != nil {
				return failed(fmt.Errorf("read how %s failed: %w", parent, err))
			}
			n.Failure = parentFailure(parent, row.FailureReason, row.Error)
		}
	} else if m != nil && m.RetryAuto {
		// safety: the attempt report cannot tell the controller a failed before_run is final, so every
		// dispatch runs before_run again and a refused hook never lets a later dispatch run the body.
		layers, n.Attempt, n.BeforeRunEachAttempt = podAutoLayers, attempt, true
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
	err := run.State.FinishNodeWithReason(writeCtx, run.ID, id, string(res.Outcome), recordedErrText(res.Err), res.Output, res.Reason, nil)
	if errors.Is(err, store.ErrOutputNotStored) {
		res = Result{Outcome: sparkwing.Failed, Err: err}
		err = run.State.FinishNodeWithReason(writeCtx, run.ID, id, string(res.Outcome), recordedErrText(err), nil, store.FailureUnknown, nil)
	}
	if err != nil {
		run.Logger.Error("nodehost: record node result", "node", id, "err", err)
		res.Outcome, res.Err = sparkwing.Failed, errors.Join(res.Err, fmt.Errorf("record node result: %w", err))
	}
	nodeEndEvent(writeCtx, run, id, res)
	return res
}
