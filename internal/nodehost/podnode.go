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
	scfg.HoldOutput, scfg.Masker = true, req.Masker
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
	err = releaseDescribed(sess, known, err)
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
		return PlannedRun{}, maskErr(req.Masker, cerr)
	}
	return out, maskErr(req.Masker, err)
}

// HostDispatchedNode executes only the layers owned by a dispatched executor.
func HostDispatchedNode(ctx context.Context, cfg Config, nodeID, acceptedHash string) Result {
	run := podRun(&cfg)
	return podHost(ctx, &cfg, run, nodeID, podAttempt(ctx, &cfg, run, nodeID, acceptedHash))
}

func podRun(cfg *Config) *Run {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Attempts != nil {
		cfg.State, cfg.Logger = maskState(cfg.State, cfg.Attempts.Masker), maskLogger(cfg.Logger, cfg.Attempts.Masker)
	}
	return newRun(cfg, nodeproto.PlanDoc{}, cfg.Args, nil)
}

func podHost(ctx context.Context, cfg *Config, run *Run, nodeID string, res Result) Result {
	if res.Outcome == sparkwing.Failed && ctx.Err() != nil {
		res.Outcome = sparkwing.Cancelled
	}
	return recordResult(ctx, run, nodeID, res)
}

func podAttempt(ctx context.Context, cfg *Config, run *Run, nodeID, acceptedHash string) Result {
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
	default:
		if n.Plan, n.Generators, err = podGenerated(ctx, run, nodeID, acceptedHash); err != nil {
			return failed(err)
		}
	}
	n.Plan.Closures.Generate = nil
	if parent := n.Plan.OnFailureOf; parent != "" {
		row, err := run.State.GetNode(ctx, run.ID, parent)
		if err != nil {
			return failed(fmt.Errorf("read how %s failed: %w", parent, err))
		}
		n.Failure = parentFailure(parent, row.FailureReason, row.Error)
	}
	return podChain(cfg, []Layer{artifactsLayer, cachesLayer, retryLayer, timeoutLayer})(ctx, n)
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
