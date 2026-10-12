package sparkwingruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/swtags"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithPipelineSecrets(ctx context.Context, v any) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.PipelineSecrets, v)
}

// MissingSecretsError names every required secret of a pipeline's Secrets()
// declaration that its secret source does not hold.
type MissingSecretsError struct {
	Pipeline string
	Names    []string
}

func (e *MissingSecretsError) Error() string {
	noun := "secrets"
	if len(e.Names) == 1 {
		noun = "secret"
	}
	return fmt.Sprintf("pipeline %q secrets: no value for required %s %s", e.Pipeline, noun, strings.Join(e.Names, ", "))
}

// Unwrap lets a caller match the miss as [sparkwing.ErrSecretMissing].
func (e *MissingSecretsError) Unwrap() error { return sparkwing.ErrSecretMissing }

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
			if !specs[i].Required && !specs[i].Optional {
				specs[i].Required = true
			}
		}
	}

	required := map[string]bool{}
	var order []string
	for _, s := range specs {
		if _, seen := required[s.Name]; !seen {
			order = append(order, s.Name)
		}
		required[s.Name] = required[s.Name] || s.Required
	}

	if len(order) > 0 && resolver == nil {
		return nil, fmt.Errorf("pipeline %q secrets: declared but no SecretResolver installed on ctx", reg.Name)
	}

	values := map[string]string{}
	missing := &MissingSecretsError{Pipeline: reg.Name}
	// safety: every required secret is resolved before the first node, and all
	// that are missing are reported together, so one fix pass covers them.
	for _, name := range order {
		v, _, err := resolver.Resolve(ctx, name)
		switch {
		case err == nil:
			values[name] = v
		case errors.Is(err, sparkwing.ErrSecretMissing) && required[name]:
			missing.Names = append(missing.Names, name)
		case errors.Is(err, sparkwing.ErrSecretMissing):
		default:
			return nil, fmt.Errorf("pipeline %q secrets: %q: %w", reg.Name, name, err)
		}
	}
	if len(missing.Names) > 0 {
		return nil, missing
	}
	for _, s := range specs {
		v, ok := values[s.Name]
		if !ok {
			continue
		}
		if err := swtags.CoerceAssign(elem.FieldByIndex(s.Field.Index), v, s.Field.Name); err != nil {
			return nil, fmt.Errorf("pipeline %q secrets: field %s expects %s", reg.Name, s.Field.Name, s.Field.Type)
		}
	}

	return sec, nil
}
