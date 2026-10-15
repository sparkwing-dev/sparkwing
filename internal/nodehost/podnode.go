package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
	// CheckArgs, when set, refuses the plan given the arguments it plans
	// with, describe's defaults filled in.
	CheckArgs    func(map[string]string) error
	CheckSecrets func(context.Context, []string) error
}

// PlannedRun contains validated SDK declarations and the host facts used to plan them.
type PlannedRun struct {
	Secrets  []string
	Requires []string
	Doc      nodeproto.PlanDoc
	JSON     []byte
	Args     map[string]string
}

// PlanDocument plans a pipeline in a short-lived SDK process, as a hosted
// run's plan process does, and validates the plan the same way.
func PlanDocument(ctx context.Context, cfg SessionConfig, req PlanRequest) (PlannedRun, error) {
	if cfg.Sink == nil {
		cfg.Sink = discardSink{}
	}
	scfg := dryRunSession(cfg, req.Run.DryRun)
	scfg.Binding = nil
	scfg.HoldOutput = true
	sess, err := StartSession(ctx, scfg)
	if err != nil {
		return PlannedRun{}, err
	}
	var out PlannedRun
	var known bool
	out.Args, known, err = describeArgs(ctx, sess, req.Pipeline, req.Args, req.Masker)
	out.Secrets = slices.Clone(sess.secrets)
	out.Requires = slices.Clone(req.Requires)
	if err == nil && req.CheckArgs != nil {
		err = req.CheckArgs(out.Args)
	}
	if err == nil && req.CheckSecrets != nil {
		err = req.CheckSecrets(ctx, slices.Clone(sess.requiredSecrets))
	}
	err = releaseDescribed(sess, known, req.Masker, err)
	if err == nil {
		out.Doc, out.JSON, err = planRun(ctx, sess, req.Pipeline, out.Args, req.Run)
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
	var perr *apierr.Error
	if err == nil && errors.As(cerr, &perr) {
		// safety: a process that broke the protocol after its reply cannot vouch for that plan.
		return PlannedRun{}, cerr
	}
	return out, err
}

var (
	podLayers = []Layer{skipIfLayer, memoLayer, admissionLayer, artifactsLayer, cachesLayer, retryLayer, timeoutLayer}

	// safety: a dispatched node's run host already ran its skip_if, memo and
	// admission, so the pod runs only executor layers.
	podDispatchedLayers = []Layer{artifactsLayer, cachesLayer, retryLayer, timeoutLayer}
)

// HostNode executes an accepted job after reconstructing its declarations and checking drift.
func HostNode(ctx context.Context, cfg Config, nodeID, acceptedHash string, attempt int) Result {
	run := podRun(&cfg)
	return podHost(ctx, &cfg, run, nodeID, podAttempt(ctx, &cfg, run, nodeID, acceptedHash, max(attempt, 1), false))
}

// HostDispatchedNode executes only the layers owned by a dispatched executor.
func HostDispatchedNode(ctx context.Context, cfg Config, nodeID, acceptedHash string) Result {
	run := podRun(&cfg)
	return podHost(ctx, &cfg, run, nodeID, podAttempt(ctx, &cfg, run, nodeID, acceptedHash, 1, true))
}

func podRun(cfg *Config) *Run {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Run{
		ID: cfg.Run.RunID, validation: cfg.Session.Validation, Pipeline: cfg.Pipeline, Args: cfg.Args, Context: cfg.Run,
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
	run.Doc, run.Args, run.Secrets, run.Requires = doc, planned.Args, planned.Secrets, planned.Requires
	i := slices.IndexFunc(doc.Jobs, func(n nodeproto.JobSpec) bool { return n.ID == nodeID })
	drift := func(format string, args ...any) Result {
		return failed(&apierr.Error{Code: apierr.PlanDrift, Message: fmt.Sprintf(format, args...)})
	}
	n := &NodeRun{Run: run}
	switch {
	case acceptedHash == "":
		return drift("the dispatcher handed node %s no accepted spec_hash", nodeID)
	case i >= 0 && jobHash(doc.Jobs[i]) != acceptedHash:
		return drift("node %s plans differently than when the run was accepted; plan code must be deterministic for a run", nodeID)
	case i >= 0:
		n.Plan = doc.Jobs[i]
	case !dispatched:
		return drift("node %s is not in this process's plan; plan code must be deterministic for a run", nodeID)
	default:
		if n.Plan, n.Generators, err = podGenerated(ctx, run, nodeID, acceptedHash); err != nil {
			return failed(err)
		}
	}
	layers := podLayers
	if dispatched {
		layers = podDispatchedLayers
		// safety: the run host generates a dispatched source's nodes from the output this host records, since only
		// it can add them to the run, so the generators do not run here as well.
		n.Plan.Closures.Generate = nil
		if parent := n.Plan.OnFailureOf; parent != "" {
			row, err := run.State.GetNode(ctx, run.ID, parent)
			if err != nil {
				return failed(fmt.Errorf("read how %s failed: %w", parent, err))
			}
			n.Failure = parentFailure(parent, row.FailureReason, row.Error)
		}
	}
	res := podChain(cfg, layers)(ctx, n)
	if !dispatched && len(res.expansions) > 0 && res.Outcome == sparkwing.Success {
		// safety: a pod cannot add nodes to its run, and dropping them would read as an empty fan-out.
		return failed(fmt.Errorf("node %s generates nodes, which a pod cannot host yet", nodeID))
	}
	return res
}

// safety: a generated node is in no process's plan, so this host runs the spec the run host recorded on its row
// when its generator returned it, as the run accepted it, with the generators that load its code in the attempt:
// each source's that added the next node, back to a node of the plan.
func podGenerated(ctx context.Context, run *Run, nodeID, acceptedHash string) (nodeproto.JobSpec, []string, error) {
	drift := func(format string, args ...any) error {
		return &apierr.Error{Code: apierr.PlanDrift, Message: fmt.Sprintf(format, args...)}
	}
	row, err := run.State.GetNode(ctx, run.ID, nodeID)
	if err != nil {
		return nodeproto.JobSpec{}, nil, fmt.Errorf("read node %s: %w", nodeID, err)
	}
	if row.Generated == nil {
		return nodeproto.JobSpec{}, nil, drift("node %s is not in this process's plan; plan code must be deterministic for a run", nodeID)
	}
	var spec nodeproto.JobSpec
	if err := json.Unmarshal(row.Generated.Spec, &spec); err != nil || spec.ID != nodeID || jobHash(spec) != acceptedHash {
		return nodeproto.JobSpec{}, nil, drift("node %s's recorded spec is not the one its run accepted (%v)", nodeID, err)
	}
	inPlan := func(id string) bool {
		return slices.ContainsFunc(run.Doc.Jobs, func(p nodeproto.JobSpec) bool { return p.ID == id })
	}
	chain := []string{row.Generated.Generator}
	seen := map[string]bool{nodeID: true}
	for source := row.Generated.Source; !inPlan(source); {
		if seen[source] {
			return nodeproto.JobSpec{}, nil, drift("node %s's recorded sources repeat %s", nodeID, source)
		}
		seen[source] = true
		src, err := run.State.GetNode(ctx, run.ID, source)
		switch {
		case err != nil:
			return nodeproto.JobSpec{}, nil, fmt.Errorf("read node %s's source %s: %w", nodeID, source, err)
		case src.Generated == nil:
			return nodeproto.JobSpec{}, nil, drift("node %s's source %s is neither in this process's plan nor generated", nodeID, source)
		}
		chain = append([]string{src.Generated.Generator}, chain...)
		source = src.Generated.Source
	}
	return spec, chain, nil
}

func podChain(cfg *Config, layers []Layer) Executor {
	inner := cfg.Attempts.executor(cfg)
	exec := func(ctx context.Context, n *NodeRun) Result {
		if err := n.Run.State.StartNode(ctx, n.Run.ID, n.Plan.ID); err != nil {
			return failed(fmt.Errorf("start node %s: %w", n.Plan.ID, err))
		}
		// safety: application retries share one node start event.
		if n.Attempt <= 1 {
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
