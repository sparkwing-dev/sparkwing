package sparkwing

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/sparkwing-dev/sparkwing/internal/swtags"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func (sdkOperations) Describe(ctx context.Context) (nodeproto.DescribeDoc, error) {
	doc := nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{}}
	for _, name := range registered() {
		reg, _ := lookup(name)
		description, err := describePipeline(ctx, reg)
		if err != nil {
			var failure *apierr.Error
			if !errors.As(err, &failure) {
				return nodeproto.DescribeDoc{}, err
			}
			description = nodeproto.PipelineDescription{Name: name, Args: []nodeproto.ArgumentDescription{}, Error: failure}
		}
		doc.Pipelines = append(doc.Pipelines, description)
	}
	return doc, nil
}

func describePipeline(ctx context.Context, reg *registration) (d nodeproto.PipelineDescription, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			err = planningFailure(failure)
		}
		if err != nil {
			err = sdkError(apierr.PlanFailed, fmt.Sprintf("describe pipeline %q: %v", reg.Name, err))
		}
	}()
	p := reg.instance()
	d = nodeproto.PipelineDescription{Name: reg.Name, Args: []nodeproto.ArgumentDescription{}, Extra: reg.Schema.Extra}
	if h, ok := p.(HelpProvider); ok {
		d.Help = h.Help()
	}
	if h, ok := p.(ShortHelpProvider); ok {
		d.Short = h.ShortHelp()
	}
	if h, ok := p.(ExampleProvider); ok {
		for _, e := range h.Examples() {
			d.Examples = append(d.Examples, nodeproto.UsageExample{Comment: e.Comment, Command: e.Command})
		}
	}
	if h, ok := p.(EnvVarDocer); ok {
		for _, e := range h.EnvVars() {
			d.EnvVars = append(d.EnvVars, nodeproto.EnvironmentDescription{Name: e.Name, Description: e.Description, Default: &e.Default})
		}
	}
	for _, f := range reg.Schema.Fields {
		if f.isExtraBag {
			continue
		}
		d.Args = append(d.Args, nodeproto.ArgumentDescription{Name: f.Name, Short: f.Short, Type: f.Type, Required: f.Required, Description: f.Description, Default: declaredDefault(f.Default), Enum: f.Enum, Secret: f.Secret})
	}
	if h, ok := p.(SecretsProvider); ok {
		fields, err := secretDeclarations(h.Secrets())
		if err != nil {
			return d, err
		}
		d.Secrets = fields
	}
	plan, err := reg.discover(ctx)
	if err != nil {
		return d, err
	}
	for _, a := range plan.declaredArguments() {
		d.Args = append(d.Args, nodeproto.ArgumentDescription{Name: a.Name, Short: a.Short, Type: a.Type, Required: a.Required, Description: a.Desc, Default: declaredDefault(a.Default), Enum: a.Enum, Secret: a.Secret, JobID: a.JobID})
	}
	riskSet := map[string]bool{}
	for _, job := range planDocument(plan).Jobs {
		for _, step := range job.Steps {
			if len(step.Risks) > 0 {
				d.RisksByStep = append(d.RisksByStep, nodeproto.StepRisks{JobID: job.ID, StepID: step.ID, Labels: step.Risks})
				for _, risk := range step.Risks {
					riskSet[risk] = true
				}
			}
		}
	}
	for risk := range riskSet {
		d.Risks = append(d.Risks, risk)
	}
	sort.Strings(d.Risks)
	return d, nil
}

func secretDeclarations(raw any) ([]nodeproto.SecretDeclaration, error) {
	if raw == nil {
		return nil, nil
	}
	v := reflect.ValueOf(raw)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("Secrets must return a nonnil struct pointer")
	}
	fields, err := swtags.Parse(v.Type())
	if err != nil {
		return nil, err
	}
	out := []nodeproto.SecretDeclaration{}
	seen := map[string]int{}
	for _, f := range fields {
		if i, ok := seen[f.Name]; ok {
			out[i].Optional = out[i].Optional && f.Optional
			continue
		}
		seen[f.Name] = len(out)
		out = append(out, nodeproto.SecretDeclaration{Name: f.Name, Optional: f.Optional})
	}
	return out, nil
}

type secretProviderKey struct{}

var keySecretProvider secretProviderKey

func resolveTypedSecrets(ctx context.Context) any {
	factory, _ := ctx.Value(keySecretProvider).(func() any)
	if factory == nil {
		return nil
	}
	provider, ok := factory().(SecretsProvider)
	if !ok {
		return nil
	}
	raw := provider.Secrets()

	if raw == nil {
		return nil
	}
	if _, err := secretDeclarations(raw); err != nil {
		panic(err)
	}
	fields, err := swtags.Parse(reflect.TypeOf(raw))
	if err != nil {
		panic(err)
	}
	v := reflect.ValueOf(raw).Elem()
	for _, f := range fields {
		value, err := Secret(ctx, f.Name)
		if err != nil {
			if f.Optional && errors.Is(err, ErrSecretMissing) {
				continue
			}
			panic(err)
		}
		if err := swtags.CoerceAssign(v.FieldByIndex(f.Field.Index), value, f.Field.Name); err != nil {
			panic(fmt.Errorf("secret field %s has an incompatible type", f.Field.Name))
		}
	}

	return raw
}

func declaredDefault(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
