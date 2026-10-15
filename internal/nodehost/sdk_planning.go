package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// PlanningProcessInput holds acquired code and host-resolved inputs. Args contains
// request/YAML winners; describe fills only absent keys. Planning has no job bearer.
type PlanningProcessInput struct {
	Entry       *source.Entry
	Entrypoint  string
	Runner      nodeproto.RunnerIdentity
	Run         nodeproto.RunContext
	Args        map[string]string
	Environment runspec.EnvSnapshot
	// SecretArgs retains classifications sealed at intake even if describe marks them public.
	SecretArgs []string
}

// PreparedPlanning supplies OwnerPlanning's cache inputs and lazy SDK planner.
// Args and SecretArgs belong in the resolved half submitted to AcceptPlan.
// Prepare owns and closes describe; Functions.Plan owns and closes a fresh process.
type PreparedPlanning struct {
	Description nodeproto.PipelineDescription
	Args        map[string]string
	SecretArgs  []string
	Input       PlanningInput
	Functions   PlanningFunctions
}

// SDKPlanner reconstructs planning inputs before PlanInitial checks the cache.
// Start must use the supplied log callback for every output stream and return
// any acquired process even when setup fails. Log must honor its context. The
// owner evaluates guards from the returned arguments before starting planning.
type SDKPlanner struct {
	Start         func(context.Context, PlanningProcessInput, func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error)
	EnsureEngine  func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error)
	Masker        *secrets.Masker
	Log           func(context.Context, nodeproto.FrameLog) error
	ValidatePlan  func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error)
	CanonicalHash func(any) (runspec.Hash, error)
	Warn          func(string)
}

// Prepare describes the selected entry, registers defaults from every pipeline
// and resolves requested arguments. It starts no plan process on a cache hit.
func (p SDKPlanner) Prepare(ctx context.Context, input PlanningProcessInput, planning PlanningInput) (PreparedPlanning, error) {
	if p.Start == nil || p.EnsureEngine == nil || p.Masker == nil || p.Log == nil {
		return PreparedPlanning{}, errors.New("SDK planning requires process, engine, masking and run-log adapters")
	}
	if input.Entry == nil || input.Run.RunID == "" || input.Run.Pipeline == "" {
		return PreparedPlanning{}, errors.New("SDK planning requires acquired code and sealed run identity")
	}
	input = clonePlanningProcessInput(input)
	if input.Entrypoint == "" {
		input.Entrypoint = input.Run.Pipeline
	}
	for _, name := range input.SecretArgs {
		p.Masker.Register(input.Args[name])
	}
	functions := (PlanningFunctions{Validate: p.ValidatePlan, Hash: p.CanonicalHash, Warn: p.Warn}).defaults()
	validate, hash := functions.Validate, functions.Hash
	functions.Validate = func(raw json.RawMessage, validation nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
		plan, err := validate(raw, validation)
		if err != nil {
			return nodeproto.PlanDoc{}, p.planningError(context.Background(), err, true)
		}
		return plan, nil
	}
	functions.Hash = func(value any) (runspec.Hash, error) {
		digest, err := hash(value)
		if err != nil {
			return "", p.planningError(context.Background(), err, true)
		}
		return digest, nil
	}
	if input.Environment.Digest != "" && planCacheDigestValid(planning.Inputs.EnvironmentDigest) {
		if !planCacheDigestValid(input.Environment.Digest) {
			planning.Inputs.EnvironmentDigest = ""
		} else {
			digest, err := functions.Hash(struct{ Host, Captured runspec.Hash }{planning.Inputs.EnvironmentDigest, input.Environment.Digest})
			if err != nil {
				return PreparedPlanning{}, err
			}
			planning.Inputs.EnvironmentDigest = digest
		}
	}
	logs := planningLogs{masker: p.Masker, sink: p.Log}
	var raw json.RawMessage
	var prepared PreparedPlanning
	err := p.process(ctx, input, &logs, true, func(ctx context.Context, session *executorSession) error {
		if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpDescribe}, &raw); err != nil {
			return err
		}
		var err error
		prepared, err = p.describe(raw, input)
		if err != nil {
			return err
		}
		return logs.release(ctx)
	})
	if err != nil {
		return PreparedPlanning{}, p.planningError(ctx, err, logs.known)
	}
	input.Args = maps.Clone(prepared.Args)
	planning.Inputs.Engine, planning.Inputs.Pipeline, planning.Inputs.Entrypoint = input.Entry.Engine.Identity, input.Run.Pipeline, input.Entrypoint
	runner := struct {
		Granted nodeproto.RunnerIdentity
		Target  releaseasset.Target
		Host    runspec.Hash
	}{input.Runner, input.Entry.Engine.Target, planning.Inputs.RunnerDigest}
	for _, field := range []struct {
		value any
		dst   *runspec.Hash
	}{{raw, &planning.Inputs.DescribeDigest}, {input.Args, &planning.Inputs.ArgsDigest}, {input.Run, &planning.Inputs.RunContextDigest}, {runner, &planning.Inputs.RunnerDigest}} {
		hash, err := functions.Hash(field.value)
		if err != nil {
			return PreparedPlanning{}, p.planningError(ctx, err, true)
		}
		*field.dst = hash
	}
	planning.Validation.Secrets = planningSecrets{prior: planning.Validation.Secrets, masker: p.Masker}
	functions.Plan = func(ctx context.Context) (json.RawMessage, error) {
		logs := planningLogs{masker: p.Masker, sink: p.Log, known: true}
		var raw json.RawMessage
		err := p.process(ctx, input, &logs, false, func(ctx context.Context, session *executorSession) error {
			return session.call(ctx, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: input.Entrypoint, Args: maps.Clone(input.Args), Run: &input.Run}, &raw)
		})
		if err != nil {
			return nil, p.planningError(ctx, err, true)
		}
		return raw, nil
	}
	prepared.Input, prepared.Functions = planning, functions
	if err := context.Cause(ctx); err != nil {
		return PreparedPlanning{}, err
	}
	return prepared, nil
}

func clonePlanningProcessInput(input PlanningProcessInput) PlanningProcessInput {
	entry := *input.Entry
	entry.Command = slices.Clone(entry.Command)
	input.Entry = &entry
	input.Args = maps.Clone(input.Args)
	input.SecretArgs = slices.Clone(input.SecretArgs)
	input.Environment.Names = slices.Clone(input.Environment.Names)
	input.Runner.Labels = slices.Clone(input.Runner.Labels)
	if input.Run.Git != nil {
		git := *input.Run.Git
		input.Run.Git = &git
	}
	if input.Run.Trigger != nil {
		trigger := *input.Run.Trigger
		if trigger.PullRequest != nil {
			pr := *trigger.PullRequest
			trigger.PullRequest = &pr
		}
		input.Run.Trigger = &trigger
	}
	return input
}

func (p SDKPlanner) process(ctx context.Context, input PlanningProcessInput, logs *planningLogs, describe bool, body func(context.Context, *executorSession) error) (err error) {
	parent := ctx
	if err := context.Cause(ctx); err != nil {
		return err
	}
	resolved, err := p.EnsureEngine(ctx, input.Entry.Engine.Identity, input.Entry.Engine.Target)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(resolved.Identity, input.Entry.Engine.Identity) {
		return executorError(apierr.SpecDrift, "planning engine differs from the acquired release")
	}
	input = clonePlanningProcessInput(input)
	input.Entry.Engine = resolved
	process, err := p.Start(ctx, input, logs.write)
	defer func() {
		var closeErr error
		if process != nil {
			closeErr = process.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limits.CleanupTimeout)
		defer cancel()
		if process != nil {
			err = errors.Join(err, closeErr, process.Cleanup(cleanupCtx))
		}
		err = errors.Join(err, logs.finish(cleanupCtx), context.Cause(parent))
	}()
	if err != nil {
		return err
	}
	if process == nil {
		return errors.New("SDK planning adapter returned no process")
	}
	if describe {
		// safety: preserve the describe budget before a plan can declare execution limits.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	session := &executorSession{process: process}
	if err := sdkHello(ctx, session, resolved.Identity, input.Runner, nil); err != nil {
		return err
	}
	err = body(ctx, session)
	return errors.Join(err, context.Cause(ctx))
}

func (p SDKPlanner) planningError(ctx context.Context, err error, known bool) error {
	code, message := apierr.SDKError, "SDK describe failed before secret declarations were known"
	var structured *apierr.Error
	if errors.As(err, &structured) {
		code = structured.Code
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = apierr.DeadlineExceeded
	}
	if known {
		message = boundedNodeErrText(p.Masker.Mask(err.Error()))
	}
	failure := &apierr.Error{Code: code, Message: message}
	if code == apierr.ProtocolMismatch {
		failure.Hint = "Rebuild .sparkwing with the matching SDK."
	}
	return errors.Join(context.Cause(ctx), failure)
}
