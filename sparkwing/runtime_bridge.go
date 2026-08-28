package sparkwing

import "context"

type runtimePlumbingKeys struct {
	DryRun            any
	Runner            any
	SpawnHandler      any
	StepRange         any
	JSONRefResolver   any
	PipelineResolver  any
	PipelineAwaiter   any
	Inputs            any
	PipelineSecrets   any
	SecretResolver    any
	Logger            any
	Node              any
	ResolvedArgs      any
	ProfileResolution any
}

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
