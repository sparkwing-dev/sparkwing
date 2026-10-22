package sparkwing

import (
	"errors"
	"fmt"
	"reflect"
)

type jobArgsDecl struct {
	jobID  string
	schema inputSchema
	goType reflect.Type
	holder argsHolder
}

func registerRecoveryArgs(p *Plan) {
	for _, job := range p.nodes {
		for recovery := range recoveryChain(job.onFailure) {
			registerJobArgs(p, recovery.id, recovery.job)
		}
	}
}

func registerJobArgs(p *Plan, id string, jobValue any) {
	holder, argsType := embeddedArgs(jobValue)
	if holder == nil || argsType == nil {
		return
	}
	p.diagnostics.collectJob(jobValue)
	schema, err := parseInputsSchema(argsType)
	if err != nil {
		panic(fmt.Sprintf("sparkwing: Job(%q): invalid WithArgs[%s]: %v", id, argsType, err))
	}
	for _, f := range schema.Fields {
		if f.isExtraBag {
			panic(fmt.Sprintf("sparkwing: Job(%q): WithArgs[%s] field %s: flag:\",extra\" is only supported on pipeline Inputs", id, argsType, f.GoName))
		}
	}
	p.jobArgs = append(p.jobArgs, jobArgsDecl{jobID: id, schema: schema, goType: argsType, holder: holder})
}

// JobArgs returns the flags that jobs embedding [WithArgs] add to the
// pipeline, in registration order, each stamped with its owning job id.
// The describe cache, --help renderer and completion read this so job
// flags share the envelope of pipeline-level Inputs fields.
func (p *Plan) declaredArguments() []describeArg {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []describeArg
	for _, d := range p.jobArgs {
		for _, f := range d.schema.Fields {
			out = append(out, describeArg{
				Name:     f.Name,
				GoName:   f.GoName,
				Short:    f.Short,
				Type:     f.Type,
				Required: f.Required,
				Desc:     f.Description,
				Default:  f.Default,
				Enum:     f.Enum,
				Secret:   f.Secret,
				JobID:    d.jobID,
			})
		}
	}
	return out
}

func resolveAndBindJobArgs(p *Plan, args map[string]string) error {
	if p == nil {
		return nil
	}
	var problems []error
	for _, d := range p.jobArgs {
		own := make(map[string]string, len(d.schema.Fields))
		for _, f := range d.schema.Fields {
			if v, ok := args[f.Name]; ok {
				own[f.Name] = v
			}
		}
		val := reflect.New(d.goType).Elem()
		if err := populateInputs(d.schema, val, own); err != nil {
			problems = append(problems, fmt.Errorf("job %q: %w", d.jobID, err))
			continue
		}
		if err := d.holder.bindFromAny(val.Interface()); err != nil {
			problems = append(problems, fmt.Errorf("job %q: %w", d.jobID, err))
		}
	}
	return errors.Join(problems...)
}
