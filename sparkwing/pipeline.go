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

// InputError is what a Registration's Invoke returns when args fail the
// declared flags of the pipeline or of a job's [WithArgs]: a missing required
// flag, a value of the wrong type or outside its enum, or a flag nothing
// declares. Any other Invoke error comes from the pipeline's Plan. Tell them
// apart with errors.As:
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
	defaults := inputSchema{Extra: schema.Extra}
	for _, field := range schema.Fields {
		if field.Default != "" || field.isExtraBag {
			defaults.Fields = append(defaults.Fields, field)
		}
	}
	invoke := func(ctx context.Context, args map[string]string, rc RunContext, discovery bool) (*Plan, error) {
		pipeKnown := map[string]bool{}
		for _, f := range schema.Fields {
			pipeKnown[f.Name] = true
		}
		var pipeArgs, extraArgs map[string]string
		if schema.Extra {
			pipeArgs = args
		} else {
			pipeArgs = make(map[string]string, len(args))
			extraArgs = make(map[string]string)
			for k, v := range args {
				if pipeKnown[k] {
					pipeArgs[k] = v
				} else {
					extraArgs[k] = v
				}
			}
		}
		var in T
		if t != nil && t.Kind() == reflect.Struct {
			inputs := schema
			if discovery {
				inputs = defaults
			}
			if err := populateInputs(inputs, reflect.ValueOf(&in).Elem(), pipeArgs); err != nil {
				return nil, &InputError{Err: fmt.Errorf("inputs for pipeline %q: %w", name, err)}
			}
		}
		p := factory()
		if p == nil {
			return nil, fmt.Errorf("sparkwing: factory for pipeline %q returned nil", name)
		}
		plan := NewPlan()
		plan.setInputs(in)
		if err := p.Plan(sdkserve.DeclarationContext(ctx), plan, in, rc); err != nil {
			return nil, err
		}
		registerRecoveryArgs(plan)
		if err := assertJobArgsDisjoint(plan, schema); err != nil {
			return nil, fmt.Errorf("pipeline %q: %w", name, err)
		}
		if len(extraArgs) > 0 {
			if err := assertJobArgsCoverage(plan, extraArgs); err != nil {
				return nil, &InputError{Err: fmt.Errorf("inputs for pipeline %q: %w", name, err)}
			}
		}
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
