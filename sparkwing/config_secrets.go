package sparkwing

import (
	"context"
	"fmt"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/swtags"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// ConfigProvider is optionally implemented by a pipeline value to
// declare its typed static-configuration struct. The orchestrator
// reads it at run start, layers values declared in pipelines.yaml
// onto the returned zero-value pointer, and installs the populated
// struct on ctx via WithPipelineConfig. Step bodies read the typed
// value with sparkwing.PipelineConfig[T](ctx).
//
//	type ReleaseConfig struct {
//	    ImageRepo string `sw:"image_repo,required"`
//	    Replicas  int    `sw:"replicas" default:"2"`
//	    Region    string `sw:"region"   default:"us-west-2"`
//	}
//
//	type Release struct{}
//
//	func (Release) Config() any { return &ReleaseConfig{} }
//
// Pipelines that don't implement this interface keep working with
// no Config surface; PipelineConfig[T](ctx) returns nil in that case.
type ConfigProvider interface {
	Config() any
}

// SecretsProvider is optionally implemented by a pipeline value to
// declare its typed secrets struct. The orchestrator reads it at run
// start, resolves every required entry against the SecretResolver
// installed on ctx, fails the run loudly if any required entry is
// missing, and otherwise installs the populated struct on ctx via
// WithPipelineSecrets. Step bodies read with
// sparkwing.PipelineSecrets[T](ctx).
//
//	type ReleaseSecrets struct {
//	    DeployToken string `sw:"DEPLOY_TOKEN,required"`
//	    SlackHook   string `sw:"SLACK_HOOK,optional"`
//	}
//
//	func (Release) Secrets() any { return &ReleaseSecrets{} }
//
// Fields default to required when neither ,required nor ,optional is
// set. Optional fields whose source returns ErrSecretMissing leave
// the struct field empty without failing the run.
type SecretsProvider interface {
	Secrets() any
}

// WithPipelineConfig installs the resolved Config struct on ctx.
// Intended for orchestrator implementations; pipeline authors don't
// construct dispatch ctx directly.
func WithPipelineConfig(ctx context.Context, v any) context.Context {
	return context.WithValue(ctx, keyPipelineConfig, v)
}

// PipelineConfig returns the typed Config struct installed on ctx,
// or nil when the pipeline doesn't implement ConfigProvider (or no
// install has happened). Panics when the installed value isn't
// assignable to *T -- matches the posture of sparkwing.Inputs[T].
func PipelineConfig[T any](ctx context.Context) *T {
	raw := ctx.Value(keyPipelineConfig)
	if raw == nil {
		return nil
	}
	v, ok := raw.(*T)
	if !ok {
		var zero T
		panic(fmt.Sprintf("sparkwing: PipelineConfig[%T]: installed config is %T, not *%T", zero, raw, zero))
	}
	return v
}

// PipelineSecrets returns the typed Secrets struct installed on ctx,
// or nil when the pipeline doesn't implement SecretsProvider. Same
// nil and type-mismatch posture as PipelineConfig.
func PipelineSecrets[T any](ctx context.Context) *T {
	raw := ctx.Value(keyPipelineSecrets)
	if raw == nil {
		return nil
	}
	v, ok := raw.(*T)
	if !ok {
		var zero T
		panic(fmt.Sprintf("sparkwing: PipelineSecrets[%T]: installed secrets is %T, not *%T", zero, raw, zero))
	}
	return v
}

// ResolvePipelineConfig builds the typed Config struct for a pipeline
// and layers values from pipelines.yaml onto it. Layering order, later
// wins per field:
//
//  1. Defaults declared via the `default:"..."` struct tag.
//  2. Pipeline.Values.Base (pipelines.yaml top-level values.base).
//  3. Pipeline.Targets[target].Values (when target is non-empty).
//  4. Trigger.Values for the matched trigger spec
//     (e.g. push.values when triggerSource == "push").
//
// Pipeline.Values.Runners is parsed but not consumed here; the
// per-runner overlay layers onto the chosen runner at dispatch time
// in a later step.
//
// Returns nil, nil when reg.instance doesn't implement
// ConfigProvider -- callers install nothing on ctx in that case.
// Errors when a `required` field is missing across every layer, when
// a yaml value can't coerce into the field's Go type, or when the
// pipeline value is not a struct pointer.
func ResolvePipelineConfig(reg *Registration, yamlEntry *pipelines.Pipeline, target, triggerSource string) (any, error) {
	if reg == nil || reg.instance == nil {
		return nil, nil
	}
	p := reg.instance()
	cp, ok := p.(ConfigProvider)
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
	specs, err := swtags.Parse(rv.Type())
	if err != nil {
		return nil, fmt.Errorf("pipeline %q config: %w", reg.Name, err)
	}

	elem := rv.Elem()

	// Layer 1: defaults.
	for _, s := range specs {
		if !s.HasDefault {
			continue
		}
		if err := swtags.CoerceAssign(elem.FieldByIndex(s.Field.Index), s.DefaultRaw, s.Field.Name); err != nil {
			return nil, fmt.Errorf("pipeline %q config: %w", reg.Name, err)
		}
	}

	// Layers 2 + 3 + 4: base values, per-target values, then the
	// matched trigger's values.
	if yamlEntry != nil {
		if err := applyValueOverlay(elem, specs, yamlEntry.Values.Base, reg.Name); err != nil {
			return nil, err
		}
		if target != "" {
			if t, ok := yamlEntry.Targets[target]; ok {
				if err := applyValueOverlay(elem, specs, t.Values, reg.Name); err != nil {
					return nil, err
				}
			}
		}
		if triggerSource != "" {
			if err := applyValueOverlay(elem, specs, yamlEntry.TriggerValues(triggerSource), reg.Name); err != nil {
				return nil, err
			}
		}
	}

	// Required check after all layers have run.
	for _, s := range specs {
		if !s.Required {
			continue
		}
		if reflect.DeepEqual(elem.FieldByIndex(s.Field.Index).Interface(), reflect.Zero(s.Field.Type).Interface()) {
			return nil, fmt.Errorf("pipeline %q config: field %q (sw:%q): not provided (required)", reg.Name, s.Field.Name, s.Name+",required")
		}
	}

	return cfg, nil
}

func applyValueOverlay(elem reflect.Value, specs []swtags.FieldSpec, values map[string]any, pipelineName string) error {
	if len(values) == 0 {
		return nil
	}
	for _, s := range specs {
		raw, ok := values[s.Name]
		if !ok {
			continue
		}
		if err := swtags.CoerceAssign(elem.FieldByIndex(s.Field.Index), raw, s.Field.Name); err != nil {
			return fmt.Errorf("pipeline %q config: %w", pipelineName, err)
		}
	}
	return nil
}
