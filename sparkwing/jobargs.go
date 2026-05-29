package sparkwing

import (
	"fmt"
)

// SchemaProvider is the optional interface a job implements to declare
// its typed args' constraints. Jobs that only need plain optional
// flags (zero constraints, just types) can skip it -- the framework
// synthesizes a zero-constraint schema from the embedded WithArgs[T]'s
// type via reflection.
//
//	func (DeployJob) Schema() (*sparkwing.Schema, error) {
//	    s := sparkwing.NewSchema[DeployArgs]()
//	    s.Field("Replicas").Required().Range(1, 100)
//	    return s.Build()
//	}
//
// Return (nil, nil) to opt out -- treated identically to not
// implementing the interface at all.
type SchemaProvider interface {
	Schema() (*Schema, error)
}

// JobArgSchemas returns every job-args schema registered against this
// plan, keyed by node id. The CLI flag registrar walks this map to
// build the union of all args a pipeline exposes; integration callers
// in internal/orchestrator consume the same map during dispatch.
//
// Nodes whose job doesn't embed WithArgs[T] are absent from the map
// rather than mapping to nil -- the absence carries the meaning.
func (p *Plan) JobArgSchemas() map[string]*Schema {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.jobArgs) == 0 {
		return nil
	}
	out := make(map[string]*Schema, len(p.jobArgs))
	for id, s := range p.jobArgs {
		out[id] = s
	}
	return out
}

// JobArgSchema returns the args schema for the named job, or nil
// when that job doesn't declare typed args.
func (p *Plan) JobArgSchema(id string) *Schema {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jobArgs[id]
}

// registerJobArgs is the internal hook called from [Job] (and the
// other registration verbs) to discover and store the job's args
// schema. The job's [SchemaProvider] method takes precedence; absent
// that we synthesize from the embedded WithArgs[T]'s type. Errors
// from either path bubble up as panics with the job id in context --
// schema construction is a registration-time concern and the panic
// surfaces at Plan() rather than at run time.
//
// Also enforces cross-job flag-name uniqueness within the plan: two
// jobs each declaring --replicas in the same pipeline is rejected
// here with a clear message naming the colliding ids. The author
// can fix it via a `flag:"override"` struct tag on one of them.
func registerJobArgs(p *Plan, id string, jobValue any) {
	holder, argsType := embeddedArgs(jobValue)
	if holder == nil || argsType == nil {
		return
	}
	var (
		schema *Schema
		err    error
	)
	if sp, ok := jobValue.(SchemaProvider); ok {
		schema, err = sp.Schema()
		if err != nil {
			panic(fmt.Sprintf("sparkwing: Job(%q): Schema() returned error: %v", id, err))
		}
		if schema != nil && schema.goType != argsType {
			panic(fmt.Sprintf(
				"sparkwing: Job(%q): Schema() returned schema for %s but WithArgs is parameterized on %s",
				id, schema.goType, argsType,
			))
		}
	}
	if schema == nil {
		schema, err = NewSchemaFromType(argsType)
		if err != nil {
			panic(fmt.Sprintf("sparkwing: Job(%q): synthesize schema for %s: %v", id, argsType, err))
		}
	}

	if err := assertNoFlagCollisions(p, id, schema); err != nil {
		panic(fmt.Sprintf("sparkwing: Job(%q): %v", id, err))
	}

	if p.jobArgs == nil {
		p.jobArgs = make(map[string]*Schema)
	}
	p.jobArgs[id] = schema
}

// assertNoFlagCollisions walks already-registered job schemas for
// flag-name collisions with the new schema. Reports the prior job
// that owns the colliding flag so the author can disambiguate
// (either rename one job's field or add a `flag:"..."` struct tag).
func assertNoFlagCollisions(p *Plan, newID string, newSchema *Schema) error {
	if newSchema == nil {
		return nil
	}
	owned := make(map[string]string, 16)
	for priorID, priorSchema := range p.jobArgs {
		for _, m := range priorSchema.fields {
			if m.Flag != "" {
				owned[m.Flag] = priorID
			}
		}
	}
	for _, m := range newSchema.fields {
		if m.Flag == "" {
			continue
		}
		if priorID, dup := owned[m.Flag]; dup {
			return fmt.Errorf(
				"flag --%s declared by both job %q and job %q; "+
					"rename one field or add a `flag:\"...\"` tag to disambiguate",
				m.Flag, priorID, newID,
			)
		}
	}
	return nil
}

// TransitiveArgsSurface returns the deduplicated map of every flag
// the plan exposes (across all its jobs that declare args), keyed by
// flag name with the owning job id. Used by the CLI flag registrar
// (task #31) and `pipeline describe --args` (a Tier 2 feature). A
// pipeline that contains zero arg-declaring jobs returns nil.
//
// The map is stable across calls for the same Plan -- registration
// is plan-time and we don't allow late additions to JobArgs.
func (p *Plan) TransitiveArgsSurface() map[string]TransitiveArg {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.jobArgs) == 0 {
		return nil
	}
	out := make(map[string]TransitiveArg, 16)
	for jobID, s := range p.jobArgs {
		for _, m := range s.fields {
			if m.Flag == "" {
				continue
			}
			out[m.Flag] = TransitiveArg{
				Flag:      m.Flag,
				JobID:     jobID,
				FieldName: m.Name,
				Desc:      m.Desc,
				Schema:    s,
			}
		}
	}
	return out
}

// TransitiveArg is one entry in [Plan.TransitiveArgsSurface]: the
// flag, the job that owns it, the underlying Go field, and the
// schema for resolving its value. Carries enough to render the
// CLI --help and the describe-tree view.
type TransitiveArg struct {
	Flag      string
	JobID     string
	FieldName string
	Desc      string
	Schema    *Schema
}

// setResolvedArgs stores the merged resolved-args map on the plan.
// Called by [resolveAndBindJobArgs] in the pipeline-registration
// invoke flow; pipeline authors don't call this directly.
func (p *Plan) setResolvedArgs(m map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolvedArgs = m
}

// ResolvedArgs returns the merged map of every job's typed-args
// resolution result, keyed by CLI flag name. Nil before
// [resolveAndBindJobArgs] runs; otherwise a shallow copy callers
// can read freely without locking.
func (p *Plan) ResolvedArgs() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.resolvedArgs) == 0 {
		return nil
	}
	out := make(map[string]any, len(p.resolvedArgs))
	for k, v := range p.resolvedArgs {
		out[k] = v
	}
	return out
}
