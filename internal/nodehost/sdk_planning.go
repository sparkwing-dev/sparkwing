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
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// PlanningProcessInput holds acquired code and host-resolved inputs. Args contains
// request/YAML winners; describe fills only absent keys. Planning has no job bearer.
type PlanningProcessInput struct {
	output      *planningLogs
	Entry       *source.Entry
	Entrypoint  string
	Runner      nodeproto.RunnerIdentity
	Run         nodeproto.RunContext
	Args        map[string]string
	Environment runspec.EnvSnapshot
	Debug       bool
	// SecretArgs retains classifications sealed at intake even if describe marks them public.
	SecretArgs []string
}

// PreparedPlanning supplies OwnerPlanning's validation and SDK planner.
// Args and SecretArgs belong in the resolved half submitted to AcceptPlan.
// Prepare owns and closes describe; Plan owns and closes a fresh process.
type PreparedPlanning struct {
	SecretRegistration runspec.SecretRegistration
	Declarations       *runspec.DeclarationGate
	Description        nodeproto.PipelineDescription
	Args               map[string]string
	SecretArgs         []string
	Input              PlanningInput
	Plan               func(context.Context) (json.RawMessage, error)
}

// SDKPlanner prepares SDK planning with the current host inputs.
// Start must use the supplied log callback for every output stream and return
// any acquired process even when setup fails. Log must honor its context. The
// owner evaluates guards from the returned arguments before starting planning.
type SDKPlanner struct {
	Start        func(context.Context, PlanningProcessInput, func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error)
	EnsureEngine func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error)
	Masker       *secrets.Masker
	Masks        *MaskRegistry
	Log          func(context.Context, nodeproto.FrameLog) error
}

// Prepare describes the selected entry, registers defaults from every pipeline
// and resolves requested arguments.
func (p SDKPlanner) Prepare(ctx context.Context, input PlanningProcessInput, planning PlanningInput) (PreparedPlanning, error) {
	return p.prepare(ctx, input, planning, false)
}

// Inspect returns private declarations and their sanitized presentation.
func (p SDKPlanner) Inspect(ctx context.Context, input PlanningProcessInput) (PreparedPlanning, error) {
	return p.prepare(ctx, input, PlanningInput{}, true)
}

func (p SDKPlanner) prepare(ctx context.Context, input PlanningProcessInput, planning PlanningInput, declarationsOnly bool) (PreparedPlanning, error) {
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
	if p.Masks == nil {
		p.Masks = NewMaskRegistry(p.Masker)
	}
	if err := p.Masks.initialize(runspec.SealedRequest{Request: runspec.Request{RequestedArgs: input.Args, SecretArgs: runspec.Optional[[]string]{Set: true, Value: input.SecretArgs}}}, nil, nil, nil); err != nil {
		return PreparedPlanning{}, err
	}
	declarationsKnown := false
	logs := planningLogs{debug: input.Debug, masker: p.Masker, sink: p.Log}
	var raw json.RawMessage
	var prepared PreparedPlanning
	err := p.process(ctx, input, &logs, true, func(ctx context.Context, session *executorSession) error {
		if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpDescribe}, &raw, ""); err != nil {
			return err
		}
		var err error
		var complete bool
		prepared, complete, err = p.describe(raw, input, declarationsOnly)
		if !complete {
			return err
		}
		return errors.Join(err, logs.release(ctx))
	})
	if err != nil {
		return PreparedPlanning{}, p.planningError(ctx, err, logs.known)
	}
	declarationsKnown = logs.known
	if prepared.Description.Error != nil {
		return PreparedPlanning{}, p.planningError(ctx, prepared.Description.Error, logs.known)
	}
	if declarationsOnly {
		return prepared, nil
	}
	input.Args = maps.Clone(prepared.Args)
	planning.declarations = prepared.Declarations
	planning.argumentDeclarations = prepared.Description.Args
	planning.Validation.Secrets = planningSecrets{prior: planning.Validation.Secrets, masker: p.Masker}
	prepared.Plan = func(ctx context.Context) (json.RawMessage, error) {
		logs := planningLogs{debug: input.Debug, masker: p.Masker, sink: p.Log, known: declarationsKnown}
		var raw json.RawMessage
		err := p.process(ctx, input, &logs, false, func(ctx context.Context, session *executorSession) error {
			return session.call(ctx, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: input.Entrypoint, Args: maps.Clone(input.Args), Run: &input.Run}, &raw, "")
		})
		if err != nil {
			return nil, p.planningError(ctx, err, logs.known)
		}
		return raw, nil
	}
	prepared.Input = planning
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
	input.output = logs
	process, err := p.Start(ctx, input, logs.write)
	defer func() {
		var closeErr error
		if process != nil {
			closeErr = process.Close()
		}
		cleanupCtx := lease.ExecutionContext(ctx)
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
	code := apierr.SDKError
	var structured *apierr.Error
	if errors.As(err, &structured) {
		code = structured.Code
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = apierr.DeadlineExceeded
	}
	if !known && !slices.ContainsFunc(apierr.Codes, func(definition apierr.Definition) bool { return definition.Code == code }) {
		code = apierr.SDKError
	}
	message := "describe failed: " + string(code) + " (details withheld: secrets not yet known)"
	if known {
		message = boundedNodeErrText(p.Masker.Mask(err.Error()))
	}
	failure := &apierr.Error{Code: code, Message: message}
	if known {
		if structured != nil {
			failure = p.Masker.Error(structured)
			failure.Message = message
		} else {
			failure = p.Masker.Error(failure)
		}
	}
	if code == apierr.ProtocolMismatch {
		failure.Hint = "Rebuild .sparkwing with the matching SDK."
	}
	return errors.Join(context.Cause(ctx), failure)
}
