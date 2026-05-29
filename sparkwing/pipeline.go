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
	// registry is keyed by *pipeline* name -- what the operator types
	// after `sparkwing run`. A single registry entry produces a *Plan
	// per invocation.
	registry = map[string]*Registration{}
	// entrypointRegistry is keyed by *entrypoint* name -- the YAML
	// `entrypoint:` field. The v0.6 redesign separates the two so one
	// entrypoint can back many pipelines: Go calls RegisterEntrypoint
	// once with the entrypoint name; YAML enumerates pipelines and
	// names the entrypoint for each.
	entrypointRegistry = map[string]*Registration{}
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
	reg := buildRegistration(name, factory, "sparkwing.Register")
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("sparkwing.Register(%q): already registered", name))
	}
	registry[name] = reg
	// Back-compat: also register as an entrypoint under the same name
	// so the v0.6 YAML-driven dispatch path can resolve a
	// `entrypoint: <name>` pipeline against this same factory.
	if _, exists := entrypointRegistry[name]; !exists {
		entrypointRegistry[name] = reg
	}
}

// buildRegistration is the private workhorse shared by [Register]
// and [RegisterEntrypoint]. It builds the schema, the invoke closure,
// and the *Registration; the caller writes the entry into the
// appropriate registry map.
//
// callerLabel is the SDK-author-facing identifier ("sparkwing.Register"
// or "sparkwing.RegisterEntrypoint") that surfaces in panic messages.
func buildRegistration[T any](name string, factory func() Pipeline[T], callerLabel string) *Registration {
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
	// Wing-owned flags are prefixed sw-* (--sw-ref, --sw-profile,
	// --sw-start-at, ...), so pipeline `flag:"..."` tags have the
	// full unprefixed namespace to themselves -- no reserved-name
	// collision check needed.

	invoke := func(ctx context.Context, args map[string]string, rc RunContext) (*Plan, error) {
		// Partition incoming args by who owns each key. The pipeline-
		// level Inputs schema gets only the keys it declares; anything
		// else is candidate input for the per-job WithArgs[T] resolver
		// that runs after Plan(). Without this split, populateInputs
		// would reject every job-declared arg (and the framework-
		// injected `target` mirror) as "unknown flag", since it has no
		// visibility into the job schemas that only exist post-Plan.
		pipeKnown := map[string]bool{}
		for _, f := range schema.Fields {
			pipeKnown[f.Name] = true
		}
		var pipeArgs, extraArgs map[string]string
		if schema.Extra {
			// Extra-bag schemas already accept arbitrary keys; no
			// partition needed and the bag soaks up anything that
			// isn't also claimed by a job (job-resolver wins by
			// virtue of running on the full map separately).
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
		// Now that Plan() ran, every WithArgs[T] job has registered
		// its schema. Validate the post-partition leftover against
		// the union of all job-declared flags; anything still
		// unclaimed is a real typo. Mirrors populateInputs's strict-
		// unknown contract for the v0.6 args surface.
		if len(extraArgs) > 0 {
			if err := assertJobArgsCoverage(plan, extraArgs); err != nil {
				return nil, fmt.Errorf("inputs for pipeline %q: %w", name, err)
			}
		}
		// v0.6: resolve every job's typed args (via WithArgs[T]) and
		// bind the result onto each job's WithArgs holder. The merged
		// args map is stored on the plan so the orchestrator can
		// install it on per-step contexts for sparkwing.Arg[T] reads.
		// Profile defaults + predicate context (Local/Remote/Profile)
		// arrive through ctx via the runtime's WithProfileResolution
		// install; when absent the resolver treats the run as local
		// with no profile defaults.
		//
		// The describe path installs SkipArgResolve(ctx) so it can
		// walk the plan's transitive args + risk labels without
		// having the resolve step error out on missing required args.
		if !skipArgResolveFromContext(ctx) {
			pr := profileResolutionFromContext(ctx)
			resolveIn := ResolveInputs{
				FlagValues:     args,
				ProfileName:    pr.Name,
				ProfileIsLocal: pr.IsLocal,
			}
			resolved, err := resolveAndBindJobArgs(plan, resolveIn)
			if err != nil {
				return nil, fmt.Errorf("pipeline %q: %w", name, err)
			}
			plan.setResolvedArgs(resolved)
		}
		return plan, nil
	}

	return &Registration{
		Name:      name,
		InputType: t,
		Schema:    schema,
		Invoke:    invoke,
		instance:  func() any { return factory() },
	}
}

// RegisterEntrypoint installs a Go work unit (the entrypoint) under
// the given type-name, matching the `entrypoint:` field in
// pipelines.yaml. One entrypoint can back many pipelines -- each
// pipeline in YAML names this entrypoint and supplies its own
// defaults / dispatch / guards / locked policy.
//
//	sparkwing.RegisterEntrypoint[DeployArgs]("Deploy", func() sparkwing.Pipeline[DeployArgs] {
//	    return Deploy{}
//	})
//
//	# .sparkwing/sparkwing.yaml
//	pipelines:
//	  - name: deploy-prod
//	    entrypoint: Deploy
//	    dispatch: { runners: [prod-pool] }
//	  - name: deploy-dev
//	    entrypoint: Deploy
//
// Both `sparkwing run deploy-prod` and `sparkwing run deploy-dev`
// resolve to this same factory after [BindPipelinesFromYAML] runs
// at the orchestrator's bootstrap.
//
// For the older one-pipeline-per-Go-entry model, [Register] is
// kept as a deprecation-marked sugar wrapper that registers the
// entrypoint AND inserts an implicit pipeline binding under the
// same name.
func RegisterEntrypoint[T any](entrypointName string, factory func() Pipeline[T]) {
	reg := buildRegistration(entrypointName, factory, "sparkwing.RegisterEntrypoint")
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := entrypointRegistry[entrypointName]; exists {
		panic(fmt.Sprintf("sparkwing.RegisterEntrypoint(%q): already registered", entrypointName))
	}
	entrypointRegistry[entrypointName] = reg
}

// BindPipelinesFromYAML walks every pipeline entry in cfg and
// installs a Registration under the pipeline's name, sharing the
// Invoke / Schema / Instance of the registered entrypoint. The
// orchestrator's bootstrap calls this after loading pipelines.yaml
// so `sparkwing run <pipeline-name>` resolves via the standard
// [Lookup] path.
//
// Pipelines whose entrypoint isn't registered are skipped silently
// (the SDK doesn't know which binaries will be linked into the
// pipeline binary); the orchestrator surfaces "pipeline X not
// registered" at lookup time.
//
// Safe to call multiple times; existing pipeline-name bindings are
// preserved (a name that was registered via the legacy [Register]
// API doesn't get clobbered by a YAML rebind).
func BindPipelinesFromYAML(cfg interface {
	EachPipeline(func(name, entrypoint string))
}) {
	if cfg == nil {
		return
	}
	cfg.EachPipeline(func(name, entrypoint string) {
		if name == "" || entrypoint == "" {
			return
		}
		registryMu.Lock()
		defer registryMu.Unlock()
		if _, exists := registry[name]; exists {
			return
		}
		ep, ok := entrypointRegistry[entrypoint]
		if !ok {
			return
		}
		// Synthesize a Registration under the pipeline name that
		// shares the entrypoint's factory. Same Schema and Invoke;
		// Name swaps to the pipeline-side name so error messages
		// surface the operator-typed identifier.
		bound := *ep
		bound.Name = name
		registry[name] = &bound
	})
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
