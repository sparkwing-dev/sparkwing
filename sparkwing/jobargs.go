package sparkwing

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type jobArgsDecl struct {
	jobID  string
	schema InputSchema
	goType reflect.Type
	holder argsHolder
}

func registerJobArgs(p *Plan, id string, jobValue any) {
	holder, argsType := embeddedArgs(jobValue)
	if holder == nil || argsType == nil {
		return
	}
	schema, err := parseInputsSchema(argsType)
	if err != nil {
		panic(fmt.Sprintf("sparkwing: Job(%q): invalid WithArgs[%s]: %v", id, argsType, err))
	}
	for _, f := range schema.Fields {
		if f.isExtraBag {
			panic(fmt.Sprintf("sparkwing: Job(%q): WithArgs[%s] field %s: flag:\",extra\" is only supported on pipeline Inputs", id, argsType, f.GoName))
		}
		// safety: secret masking reads the pipeline Inputs schema before the
		// plan exists, so a job-level secret would reach the logs unmasked.
		if f.Secret {
			panic(fmt.Sprintf("sparkwing: Job(%q): WithArgs[%s] field %s: secret:\"true\" is only supported on pipeline Inputs", id, argsType, f.GoName))
		}
		for _, prior := range p.jobArgs {
			for _, pf := range prior.schema.Fields {
				if pf.Name == f.Name {
					panic(fmt.Sprintf(
						"sparkwing: Job(%q): flag --%s declared by both job %q and job %q; "+
							"rename one with a `flag:\"...\"` tag",
						id, f.Name, prior.jobID, id,
					))
				}
				if f.Short != "" && pf.Short == f.Short {
					panic(fmt.Sprintf("sparkwing: Job(%q): short -%s declared by both job %q and job %q", id, f.Short, prior.jobID, id))
				}
			}
		}
	}
	p.jobArgs = append(p.jobArgs, jobArgsDecl{jobID: id, schema: schema, goType: argsType, holder: holder})
}

// JobArgs returns the flags that jobs embedding [WithArgs] add to the
// pipeline, in registration order, each stamped with its owning job id.
// The describe cache, --help renderer and completion read this so job
// flags share the envelope of pipeline-level Inputs fields.
func (p *Plan) JobArgs() []DescribeArg {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []DescribeArg
	for _, d := range p.jobArgs {
		for _, f := range d.schema.Fields {
			out = append(out, DescribeArg{
				Name:     f.Name,
				GoName:   f.GoName,
				Short:    f.Short,
				Type:     f.Type,
				Required: f.Required,
				Desc:     f.Description,
				Default:  f.Default,
				Enum:     f.Enum,
				JobID:    d.jobID,
			})
		}
	}
	return out
}

func assertJobArgsCoverage(p *Plan, extra map[string]string) error {
	if p == nil || len(extra) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, d := range p.jobArgs {
		for _, f := range d.schema.Fields {
			known[f.Name] = true
		}
	}
	var unknown []string
	for k := range extra {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	if len(unknown) == 1 {
		return fmt.Errorf("unknown flag --%s", unknown[0])
	}
	return fmt.Errorf("unknown flags: --%s", strings.Join(unknown, ", --"))
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
		if err := d.holder.BindFromAny(val.Interface()); err != nil {
			problems = append(problems, fmt.Errorf("job %q: %w", d.jobID, err))
		}
	}
	return errors.Join(problems...)
}

type keySkipArgResolveType struct{}

var keySkipArgResolve = keySkipArgResolveType{}

// SkipArgResolve marks ctx so a registration's Invoke builds the plan
// without resolving and binding [WithArgs] values. Describe-time
// callers use it to walk the plan graph without failing on missing
// required job args; run paths never set it, so a missing required
// arg fails before any step runs.
func SkipArgResolve(ctx context.Context) context.Context {
	return context.WithValue(ctx, keySkipArgResolve, true)
}

func skipArgResolveFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(keySkipArgResolve).(bool)
	return v
}
