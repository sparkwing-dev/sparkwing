package sparkwing

import "context"

// runtimePlumbingKeys bundles the context keys that internal/sparkwingruntime
// needs in order to install and read the orchestrator-facing values
// (dry-run flag, runner info, step range, spawn handler, ref
// resolvers). Holding the keys in one struct keeps the public surface
// of this package small: a pipeline author sees a single
// `RuntimePlumbing` entry in autocomplete rather than seven.
type runtimePlumbingKeys struct {
	DryRun           any
	Runner           any
	SpawnHandler     any
	StepRange        any
	RefResolver      any
	JSONRefResolver  any
	PipelineResolver any
	PipelineAwaiter  any
	Inputs           any
	PipelineSecrets  any
	SecretResolver   any
	Logger           any
	Node             any
	// ResolvedArgs carries the v0.6 typed-args resolution result --
	// a map keyed by flag name with each resolved Go value. Reads
	// via sparkwing.Arg[T](ctx, name). The framework installs it on
	// the run context after running Schema.Resolve.
	ResolvedArgs any

	// ProfileResolution carries the active profile's default-args,
	// name, and local/remote flag for the v0.6 args resolver. The
	// orchestrator installs it on the plan-time ctx so the
	// registration's invoke() can feed it into ResolveInputs. Read
	// path is internal to the sparkwing package.
	ProfileResolution any
}

// runtimePlumbingFns bundles function pointers to unexported runtime-
// mutator methods on author-facing types (Plan, JobGroup, WorkStep,
// SpawnSpec). Holding them here lets internal/orchestrator drive plan
// execution without those methods appearing in autocomplete or godoc
// on the author surface.
type runtimePlumbingFns struct {
	PlanInsertChild        func(p *Plan, child *JobNode) error
	PlanInsertExpanded     func(p *Plan, source *JobNode, children []*JobNode) error
	JobGroupFinalize       func(g *JobGroup, members []*JobNode, err error)
	WorkStepFn             func(s *WorkStep) func(ctx context.Context) (any, error)
	WorkStepMarkDone       func(s *WorkStep, out any)
	SpawnSpecSetResolvedID func(s *SpawnSpec, id string)
	SpawnSpecMarkDone      func(s *SpawnSpec, out any)
}

// RuntimePlumbing exposes context keys and runtime-mutator function
// pointers to internal/sparkwingruntime and internal/orchestrator so
// those packages can install context values and drive plan execution
// without a circular import or exposing the mutators on author-facing
// types.
//
// Pipeline authors should NOT reach for it. The supported surface is
// the typed accessors: IsDryRun, Runner, Ref[T].Get, and the
// SpawnHandler / WorkStep methods.
var RuntimePlumbing = struct {
	Keys runtimePlumbingKeys
	Fns  runtimePlumbingFns
}{
	Keys: runtimePlumbingKeys{
		DryRun:            dryRunKey{},
		Runner:            runnerCtxKey{},
		SpawnHandler:      keySpawnHandler,
		StepRange:         stepRangeKey{},
		RefResolver:       keyRefResolver,
		JSONRefResolver:   keyJSONRefResolver,
		PipelineResolver:  keyPipelineResolver,
		PipelineAwaiter:   keyPipelineAwaiter,
		Inputs:            keyInputs,
		PipelineSecrets:   keyPipelineSecrets,
		SecretResolver:    keySecretResolver,
		Logger:            keyLogger,
		Node:              keyNode,
		ResolvedArgs:      keyResolvedArgs,
		ProfileResolution: keyProfileResolution,
	},
	Fns: runtimePlumbingFns{
		PlanInsertChild:        (*Plan).insertChild,
		PlanInsertExpanded:     (*Plan).insertExpanded,
		JobGroupFinalize:       (*JobGroup).finalize,
		WorkStepFn:             func(s *WorkStep) func(ctx context.Context) (any, error) { return s.fn },
		WorkStepMarkDone:       (*WorkStep).markDone,
		SpawnSpecSetResolvedID: (*SpawnSpec).setResolvedID,
		SpawnSpecMarkDone:      (*SpawnSpec).markDone,
	},
}
