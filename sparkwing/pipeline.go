package sparkwing

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"sync"

	// safety: initializes before every package that imports the SDK, so no
	// init there can start a command that inherits the protocol pipes.
	_ "github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

// Base is the marker embedded by every pipeline.
type Base struct{}

type registration struct {
	Name string

	// InputType is the reflect.Type of the struct Schema describes.
	InputType reflect.Type

	// Schema is the resolved input description, parsed once at
	// registration.
	Schema inputSchema

	// Invoke is the type-erased entry point: parse the wire-format
	// args map into the typed Inputs struct, instantiate a fresh
	// pipeline, and call its Plan.
	Invoke func(ctx context.Context, args map[string]string, rc RunContext) (*Plan, error)

	discover func(context.Context) (*Plan, error)
	instance func() any
}

var (
	registryMu sync.RWMutex

	registry = map[string]*registration{}
)

// InputError reports a typed decoding failure in pipeline Inputs or a job's
// [WithArgs]. The host validates arguments before invoking the SDK.
// Use errors.As to distinguish decoding failures from errors in Plan:
//
//	var inErr *sparkwing.InputError
//	if errors.As(err, &inErr) { ... }
type InputError struct {
	Err error
}

func (e *InputError) Error() string { return e.Err.Error() }

func (e *InputError) Unwrap() error { return e.Err }

// Register installs a pipeline under the given name. The factory is
// called once per invocation to produce a fresh instance, avoiding
// shared state across runs.
//
// The Inputs type T is the pipeline's typed flag schema. Pipelines
// that take no flags use sparkwing.NoInputs. The schema is resolved
// once at registration.
//
//	type Inputs struct {
//	    SkipTests bool   `flag:"skip-tests" desc:"skip tests"`
//	    Target    string `flag:"target" default:"local"`
//	}
//
//	sparkwing.Register[Inputs]("deploy", func() sparkwing.Pipeline[Inputs] {
//	    return Deploy{}
//	})
//
// An anonymous embedded struct in Inputs is walked recursively, and the
// outermost declaration wins on a name conflict.
func Register[T any](name string, factory func() Pipeline[T]) {
	reg := buildRegistration(name, factory, "sparkwing.Register")
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("sparkwing.Register(%q): already registered", name))
	}
	registry[name] = reg
}

func buildRegistration[T any](name string, factory func() Pipeline[T], callerLabel string) *registration {
	if name == "" {
		panic(callerLabel + ": name must not be empty")
	}
	if factory == nil {
		panic(callerLabel + ": factory must not be nil")
	}
	if factory() == nil {
		panic(fmt.Sprintf("%s(%q): factory returned nil", callerLabel, name))
	}

	var zero T
	t := reflect.TypeOf(zero)
	schema, err := parseInputsSchema(t)
	if err != nil {
		panic(fmt.Sprintf("%s(%q): invalid Inputs schema on %s: %v", callerLabel, name, t, err))
	}
	invoke := func(ctx context.Context, args map[string]string, rc RunContext, discovery bool) (*Plan, error) {
		var in T
		if t != nil && t.Kind() == reflect.Struct {
			if err := populateInputs(schema, reflect.ValueOf(&in).Elem(), args); err != nil {
				return nil, &InputError{Err: fmt.Errorf("inputs for pipeline %q: %w", name, err)}
			}
		}
		p := factory()
		if p == nil {
			return nil, fmt.Errorf("sparkwing: factory for pipeline %q returned nil", name)
		}
		plan := NewPlan()
		plan.diagnostics, _ = ctx.Value(planningDiagnosticsKey{}).(*planningDiagnostics)
		plan.setInputs(in)
		if err := p.Plan(sdkserve.DeclarationContext(ctx), plan, in, rc); err != nil {
			return nil, err
		}
		registerRecoveryArgs(plan)
		if !discovery {
			if err := resolveAndBindJobArgs(plan, args); err != nil {
				return nil, &InputError{Err: fmt.Errorf("pipeline %q: %w", name, err)}
			}
		}
		plan.args = maps.Clone(args)
		return plan, nil
	}

	return &registration{
		Name:      name,
		InputType: t,
		Schema:    schema,
		Invoke: func(ctx context.Context, args map[string]string, rc RunContext) (*Plan, error) {
			return invoke(ctx, args, rc, false)
		},
		discover: func(ctx context.Context) (*Plan, error) {
			return invoke(ctx, nil, RunContext{Pipeline: name}, true)
		},
		instance: func() any { return factory() },
	}
}

// RegisterEntrypoint declares a named SDK entrypoint.
func RegisterEntrypoint[T any](entrypointName string, factory func() Pipeline[T]) {
	reg := buildRegistration(entrypointName, factory, "sparkwing.RegisterEntrypoint")
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[entrypointName]; exists {
		panic(fmt.Sprintf("sparkwing.RegisterEntrypoint(%q): already registered", entrypointName))
	}
	registry[entrypointName] = reg
}

func lookup(name string) (*registration, bool) {
	registryMu.RLock()
	r, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, false
	}
	return r, true
}

// Registered returns the names of all registered pipelines, sorted.
func registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
