package sparkwing

import (
	"context"
	"fmt"
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

// Registration is the registry's record for one pipeline. Produced
// by [Register]; consumed by the orchestrator (via [Lookup]) and CLI
// introspection (via the [InputSchema] in Schema).
type registration struct {
	// Name is the invocation name (e.g. "lint", "build-test-deploy").
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
// once at registration and cached on the returned Registration.
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
	invoke := func(ctx context.Context, args map[string]string, rc RunContext) (*Plan, error) {
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
			if err := populateInputs(schema, reflect.ValueOf(&in).Elem(), pipeArgs); err != nil {
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
		for _, job := range plan.nodes {
			if recovery := job.onFailure; recovery != nil {
				registerJobArgs(plan, recovery.id, recovery.job)
			}
		}
		if err := assertJobArgsDisjoint(plan, schema); err != nil {
			return nil, fmt.Errorf("pipeline %q: %w", name, err)
		}
		if len(extraArgs) > 0 {
			if err := assertJobArgsCoverage(plan, extraArgs); err != nil {
				return nil, &InputError{Err: fmt.Errorf("inputs for pipeline %q: %w", name, err)}
			}
		}
		if !skipArgResolveFromContext(ctx) {
			if err := resolveAndBindJobArgs(plan, args); err != nil {
				return nil, &InputError{Err: fmt.Errorf("pipeline %q: %w", name, err)}
			}
		}
		return plan, nil
	}

	return &registration{
		Name:      name,
		InputType: t,
		Schema:    schema,
		Invoke:    invoke,
		instance:  func() any { return factory() },
	}
}

// RegisterEntrypoint declares a named SDK entrypoint. The host maps project
// pipeline names to this entrypoint before requesting a plan.
func RegisterEntrypoint[T any](entrypointName string, factory func() Pipeline[T]) {
	reg := buildRegistration(entrypointName, factory, "sparkwing.RegisterEntrypoint")
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[entrypointName]; exists {
		panic(fmt.Sprintf("sparkwing.RegisterEntrypoint(%q): already registered", entrypointName))
	}
	registry[entrypointName] = reg
}

// Lookup returns the Registration for a registered pipeline name, or
// ok=false if none. The returned Registration is shared and should be
// treated as read-only; call Invoke to drive the pipeline.
func lookup(name string) (*registration, bool) {
	registryMu.RLock()
	r, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, false
	}
	return r, true
}

// SecretValues resolves the schema's secret-marked Inputs fields
// against the wire-format args map (applying tag-declared defaults
// for unset keys) and returns the resolved string values. Empty
// values are skipped. Bag-field secrets are out of scope: `,extra`
// bags carry arbitrary keys with no per-key opt-in.
func (r *registration) secretValues(args map[string]string) []string {
	var out []string
	for _, f := range r.Schema.Fields {
		if !f.Secret || f.isExtraBag {
			continue
		}
		v, ok := args[f.Name]
		if !ok {
			v = f.Default
		}
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
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
