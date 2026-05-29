package sparkwing

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

// Base is the marker embedded by every pipeline. Reserved for future
// shared metadata helpers; today it has no fields.
type Base struct{}

// Registration is the registry's record for one pipeline. Produced
// by [Register]; consumed by the orchestrator (via [Lookup]) and CLI
// introspection (via the [InputSchema] in Schema).
type Registration struct {
	// Name is the invocation name (e.g. "lint", "build-test-deploy").
	Name string

	// InputType is the reflect.Type of the pipeline's Inputs struct,
	// retained for introspection. Same struct described by Schema.
	InputType reflect.Type

	// Schema is the resolved input description, parsed once at
	// registration. CLI describe / --help / completion / dashboard
	// run-form / MCP tool definitions all read from Schema.
	Schema InputSchema

	// Invoke is the type-erased entry point: parse the wire-format
	// args map into the typed Inputs struct, instantiate a fresh
	// pipeline, and call its Plan.
	Invoke func(ctx context.Context, args map[string]string, rc RunContext) (*Plan, error)

	// instance returns a fresh pipeline value, used by introspection
	// helpers that need to look at provider interfaces (HelpProvider,
	// ShortHelpProvider, ExampleProvider).
	instance func() any
}

var (
	registryMu sync.RWMutex
	registry   = map[string]*Registration{}
)

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
// Use sparkwing.NoInputs for pipelines that take no flags:
//
//	sparkwing.Register[sparkwing.NoInputs]("lint", func() sparkwing.Pipeline[sparkwing.NoInputs] {
//	    return Lint{}
//	})
//
// Anonymous embedded structs in Inputs are walked recursively, so
// shared flag bundles can be reused across pipelines. The outermost
// declaration wins on name conflicts (Go embedding shadowing).
//
//	type SkipFilterArgs struct {
//	    Skip string `flag:"skip"`
//	    Only string `flag:"only"`
//	}
//	type ReleaseArgs struct {
//	    Version string `flag:"version"`
//	    SkipFilterArgs   // --skip and --only become first-class flags
//	}
func Register[T any](name string, factory func() Pipeline[T]) {
	if name == "" {
		panic("sparkwing.Register: name must not be empty")
	}
	if factory == nil {
		panic("sparkwing.Register: factory must not be nil")
	}
	if factory() == nil {
		panic(fmt.Sprintf("sparkwing.Register(%q): factory returned nil", name))
	}

	var zero T
	t := reflect.TypeOf(zero)
	schema, err := parseInputsSchema(t)
	if err != nil {
		panic(fmt.Sprintf("sparkwing.Register(%q): invalid Inputs schema on %s: %v", name, t, err))
	}
	// Wing-owned flags are prefixed sw-* (--sw-ref, --sw-profile,
	// --sw-start-at, ...), so pipeline `flag:"..."` tags have the
	// full unprefixed namespace to themselves -- no reserved-name
	// collision check needed.

	invoke := func(ctx context.Context, args map[string]string, rc RunContext) (*Plan, error) {
		var in T
		if t != nil && t.Kind() == reflect.Struct {
			if err := populateInputs(schema, reflect.ValueOf(&in).Elem(), args); err != nil {
				return nil, fmt.Errorf("inputs for pipeline %q: %w", name, err)
			}
		}
		p := factory()
		if p == nil {
			return nil, fmt.Errorf("sparkwing: factory for pipeline %q returned nil", name)
		}
		// Mark ctx as plan-time so side-effect helpers panic if Plan()
		// shells out instead of declaring a node that does the work.
		plan := NewPlan()
		// Capture the parsed Inputs on the Plan so the orchestrator
		// can install them on dispatch ctx -- step bodies then read
		// the same value via sparkwing.Inputs[T](ctx) without closure
		// threading.
		plan.setInputs(in)
		if err := p.Plan(planguard.With(ctx), plan, in, rc); err != nil {
			return nil, err
		}
		// v0.6: resolve every job's typed args (via WithArgs[T]) and
		// bind the result onto each job's WithArgs holder. The merged
		// args map is stored on the plan so the orchestrator can
		// install it on per-step contexts for sparkwing.Arg[T] reads.
		// Profile defaults + predicate context get plumbed from the
		// outer CLI in a follow-up; for now the resolve sees CLI args
		// only, which still drives Required / Default / Computed.
		resolveIn := ResolveInputs{FlagValues: args}
		resolved, err := resolveAndBindJobArgs(plan, resolveIn)
		if err != nil {
			return nil, fmt.Errorf("pipeline %q: %w", name, err)
		}
		plan.setResolvedArgs(resolved)
		return plan, nil
	}

	reg := &Registration{
		Name:      name,
		InputType: t,
		Schema:    schema,
		Invoke:    invoke,
		instance:  func() any { return factory() },
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("sparkwing.Register(%q): already registered", name))
	}
	registry[name] = reg
}

// Lookup returns the Registration for a registered pipeline name, or
// ok=false if none. The returned Registration is shared and should be
// treated as read-only; call Invoke to drive the pipeline.
func Lookup(name string) (*Registration, bool) {
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
func (r *Registration) SecretValues(args map[string]string) []string {
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

// Instance returns a fresh pipeline value for this registration, used
// by introspection helpers that query optional provider interfaces
// (HelpProvider, ShortHelpProvider, ExampleProvider). The orchestrator
// goes through Registration.Invoke instead.
//
// Exposed for internal/sparkwingruntime.
func (r *Registration) Instance() any {
	if r == nil || r.instance == nil {
		return nil
	}
	return r.instance()
}

// Registered returns the names of all registered pipelines, sorted.
func Registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// TypeName returns the Go type name of p, suitable for matching against
// a pipelines.yaml `entrypoint:` field.
func TypeName(p any) string {
	t := reflect.TypeOf(p)
	if t == nil {
		return ""
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}
