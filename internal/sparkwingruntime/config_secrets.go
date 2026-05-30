package sparkwingruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/swtags"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithPipelineSecrets installs the resolved Secrets struct on ctx.
func WithPipelineSecrets(ctx context.Context, v any) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.PipelineSecrets, v)
}

// ResolvePipelineSecrets resolves every required secret declared by
// the pipeline's SecretsProvider against the SecretResolver
// installed on ctx, before the pipeline's Plan runs.
//
// Required fail-fast: a missing required secret produces a clear
// error naming the pipeline and the secret. Optional entries whose
// resolver error matches ErrSecretMissing leave the struct field
// empty without failing the run; other errors propagate.
//
// Resolved values populate the Secrets struct fields by sw-tag name,
// so step bodies can read sec.DeployToken directly via
// PipelineSecrets[T](ctx) without re-fetching.
//
// Returns (nil, nil) when the pipeline value does not implement
// SecretsProvider -- nothing to install.
func ResolvePipelineSecrets(ctx context.Context, reg *sparkwing.Registration, _ *pipelines.Pipeline) (any, error) {
	if reg == nil {
		return nil, nil
	}
	p := reg.Instance()
	if p == nil {
		return nil, nil
	}
	resolver, _ := ctx.Value(sparkwing.RuntimePlumbing.Keys.SecretResolver).(sparkwing.SecretResolver)

	sp, hasProvider := p.(sparkwing.SecretsProvider)
	if !hasProvider {
		return nil, nil
	}

	var sec any
	var specs []swtags.FieldSpec
	var elem reflect.Value
	sec = sp.Secrets()
	if sec != nil {
		rv := reflect.ValueOf(sec)
		if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
			return nil, fmt.Errorf("pipeline %q secrets: Secrets() must return a non-nil pointer to a struct, got %T", reg.Name, sec)
		}
		ss, err := swtags.Parse(rv.Type())
		if err != nil {
			return nil, fmt.Errorf("pipeline %q secrets: %w", reg.Name, err)
		}
		specs = ss
		elem = rv.Elem()
		for i := range specs {
			// Secrets default to required when neither flag is set.
			if !specs[i].Required && !specs[i].Optional {
				specs[i].Required = true
			}
		}
	}

	// Track required-ness per name.
	requiredNames := map[string]struct{}{}
	optionalNames := map[string]struct{}{}
	for _, s := range specs {
		if s.Required {
			requiredNames[s.Name] = struct{}{}
			delete(optionalNames, s.Name)
		} else if s.Optional {
			if _, alreadyReq := requiredNames[s.Name]; !alreadyReq {
				optionalNames[s.Name] = struct{}{}
			}
		}
	}

	if (len(requiredNames) > 0 || len(optionalNames) > 0) && resolver == nil {
		return nil, fmt.Errorf("pipeline %q secrets: declared but no SecretResolver installed on ctx", reg.Name)
	}

	// Resolve every name, populate the struct field when one exists.
	specByName := map[string]swtags.FieldSpec{}
	for _, s := range specs {
		specByName[s.Name] = s
	}

	// Required first so the run fails before any optional resolution.
	for name := range requiredNames {
		v, _, err := resolver.Resolve(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("pipeline %q secrets: %q: %w", reg.Name, name, err)
		}
		if s, ok := specByName[name]; ok {
			if err := swtags.CoerceAssign(elem.FieldByIndex(s.Field.Index), v, s.Field.Name); err != nil {
				return nil, fmt.Errorf("pipeline %q secrets: %w", reg.Name, err)
			}
		}
	}
	for name := range optionalNames {
		v, _, err := resolver.Resolve(ctx, name)
		if err != nil {
			if errors.Is(err, sparkwing.ErrSecretMissing) {
				continue
			}
			return nil, fmt.Errorf("pipeline %q secrets: %q: %w", reg.Name, name, err)
		}
		if s, ok := specByName[name]; ok {
			if err := swtags.CoerceAssign(elem.FieldByIndex(s.Field.Index), v, s.Field.Name); err != nil {
				return nil, fmt.Errorf("pipeline %q secrets: %w", reg.Name, err)
			}
		}
	}

	return sec, nil
}
