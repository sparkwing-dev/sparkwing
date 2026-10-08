package sparkwing

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
)

type runtimePlumbingKeys struct {
	DryRun           any
	Runner           any
	StepRange        any
	JSONRefResolver  any
	PipelineResolver any
	PipelineAwaiter  any
	Inputs           any
	PipelineSecrets  any
	SecretResolver   any
	Logger           any
	Node             any
	Admission        any
	OIDCTokenSource  any
}

type runtimePlumbingFns struct {
	PlanInsertExpanded func(p *Plan, source *JobNode, children []*JobNode) error
	JobGroupFinalize   func(g *JobGroup, members []*JobNode, err error)
	WorkStepFn         func(s *WorkStep) func(ctx context.Context) (any, error)
	WorkStepMarkDone   func(s *WorkStep, out any)
	NodeDirCaches      func(n *JobNode) []depcache.Spec
}

// RuntimePlumbing exposes context keys and runtime-mutator function
// pointers to internal/sparkwingruntime and internal/orchestrator so
// those packages can install context values and drive plan execution
// without a circular import or exposing the mutators on author-facing
// types.
//
// Pipeline authors should NOT reach for it. The supported surface is
// the typed accessors: IsDryRun, Runner, Admitted, Ref[T].Get, and
// the WorkStep methods.
var RuntimePlumbing = struct {
	Keys runtimePlumbingKeys
	Fns  runtimePlumbingFns
}{
	Keys: runtimePlumbingKeys{
		DryRun:           dryRunKey{},
		Runner:           runnerCtxKey{},
		StepRange:        stepRangeKey{},
		JSONRefResolver:  keyJSONRefResolver,
		PipelineResolver: keyPipelineResolver,
		PipelineAwaiter:  keyPipelineAwaiter,
		Inputs:           keyInputs,
		PipelineSecrets:  keyPipelineSecrets,
		SecretResolver:   keySecretResolver,
		Logger:           keyLogger,
		Node:             keyNode,
		Admission:        keyAdmission,
		OIDCTokenSource:  oidcTokenSourceKey{},
	},
	Fns: runtimePlumbingFns{
		PlanInsertExpanded: (*Plan).insertExpanded,
		JobGroupFinalize:   (*JobGroup).finalize,
		WorkStepFn:         func(s *WorkStep) func(ctx context.Context) (any, error) { return s.fn },
		WorkStepMarkDone:   (*WorkStep).markDone,
		NodeDirCaches:      dirCacheSpecs,
	},
}
