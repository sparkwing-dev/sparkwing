package sparkwingruntime

import (
	"context"
	"encoding/json"
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

// DecodePipelineConfig rehydrates a previously-resolved Config
// struct from a JSON blob. The struct's typed shape comes from the
// pipeline's Config() factory; the blob carries only values. Used
// by the cluster pod path to restore the typed Config the
// orchestrator-side resolution produced without re-running the
// yaml-layering logic on the pod.
//
// Returns (nil, nil) when the pipeline does not implement
// ConfigProvider or the blob is empty.
func DecodePipelineConfig(reg *sparkwing.Registration, raw []byte) (any, error) {
	if reg == nil || len(raw) == 0 {
		return nil, nil
	}
	inst := reg.Instance()
	if inst == nil {
		return nil, nil
	}
	cp, ok := inst.(sparkwing.ConfigProvider)
	if !ok {
		return nil, nil
	}
	cfg := cp.Config()
	if cfg == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(cfg)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("pipeline %q config: Config() must return a non-nil pointer to a struct, got %T", reg.Name, cfg)
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("pipeline %q config: decode persisted blob: %w", reg.Name, err)
	}
	return cfg, nil
}

// ResolvePipelineSecrets resolves every required secret declared by
// the pipeline -- both via the SecretsProvider's struct fields and
// via the SecretsField list on the yaml entry -- against the
// SecretResolver installed on ctx, before the pipeline's Plan runs.
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
// Returns nil, nil when the pipeline value does not implement
// SecretsProvider and the yaml entry declares no required secrets --
// nothing to install. Returns the populated struct (or a synthesized
// zero struct when only the yaml side declares secrets) otherwise.
func ResolvePipelineSecrets(ctx context.Context, reg *sparkwing.Registration, yamlEntry *pipelines.Pipeline) (any, error) {
	if reg == nil {
		return nil, nil
	}
	p := reg.Instance()
	if p == nil {
		return nil, nil
	}
	resolver, _ := ctx.Value(sparkwing.RuntimePlumbing.Keys.SecretResolver).(sparkwing.SecretResolver)

	// SecretsField from yaml: union with the struct-declared required set.
	var yamlRequired []string
	var yamlOptional []string
	if yamlEntry != nil {
		for _, e := range yamlEntry.Secrets {
			if e.IsRequired() {
				yamlRequired = append(yamlRequired, e.Name)
			} else {
				yamlOptional = append(yamlOptional, e.Name)
			}
		}
	}

	sp, hasProvider := p.(sparkwing.SecretsProvider)
	if !hasProvider && len(yamlRequired) == 0 && len(yamlOptional) == 0 {
		return nil, nil
	}

	var sec any
	var specs []swtags.FieldSpec
	var elem reflect.Value
	if hasProvider {
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
				// Secrets default to required when neither flag is set,
				// matching the bare-string SecretsField rule.
				if !specs[i].Required && !specs[i].Optional {
					specs[i].Required = true
				}
			}
		}
	}

	// Build the union of names to resolve, tracking required-ness per
	// name. Struct entries win on conflict because they carry the
	// destination field.
	requiredNames := map[string]struct{}{}
	optionalNames := map[string]struct{}{}
	for _, n := range yamlRequired {
		requiredNames[n] = struct{}{}
	}
	for _, n := range yamlOptional {
		if _, alreadyReq := requiredNames[n]; alreadyReq {
			continue
		}
		optionalNames[n] = struct{}{}
	}
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
