package sparkwing

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
)

// ErrSecretMissing classifies a "no entry for this name" outcome from
// a SecretResolver as distinct from a transport / authorization error.
// SDK helpers like ResolvePipelineSecrets treat optional fields whose
// resolver error matches this sentinel as silently empty; other
// errors surface as run-start failures.
//
// Every secrets backend returns this same value, so
// `errors.Is(err, sparkwing.ErrSecretMissing)` matches a miss from any of
// them.
var ErrSecretMissing = secretname.ErrMissing

// SecretResolver resolves a stored value to (plain, masked) at the
// moment of the call. The orchestrator installs a resolver on the
// run ctx; jobs read values through the package-level Secret / Variable
// helpers.
//
// Resolution is on-demand: no plan-time declaration, no dispatch-time
// pre-injection. Implementations are expected to cache successful
// results per-run and register masked values with the run's log masker.
//
// The masked return drives strict matching at the SDK call site:
// Secret errors on masked=false entries, Variable errors on masked=true
// ones, so classification drift surfaces loudly.
type secretResolver interface {
	resolve(ctx context.Context, name string) (value string, masked bool, err error)
}

// SecretResolverFunc adapts a function to SecretResolver.
type secretResolverFunc func(ctx context.Context, name string) (value string, masked bool, err error)

// Resolve satisfies SecretResolver.
func (f secretResolverFunc) resolve(ctx context.Context, name string) (string, bool, error) {
	return f(ctx, name)
}

const keySecretResolver ctxKey = 100

// WithSecretResolver returns a derived ctx carrying the given resolver.
// The orchestrator installs one per run; jobs reach it via Secret.
func withSecretResolver(ctx context.Context, r secretResolver) context.Context {
	return context.WithValue(ctx, keySecretResolver, r)
}

func secretResolverFromContext(ctx context.Context) secretResolver {
	if r, ok := ctx.Value(keySecretResolver).(secretResolver); ok {
		return r
	}
	return nil
}

// Secret resolves a masked value through the resolver installed on
// ctx. Errors when no resolver is installed, the source can't produce
// a value, or the entry exists but was stored with masked=false (use
// Variable for those). Lazy by design: a misspelled name fails at the
// call site, not at run start.
//
//	token, err := sparkwing.Secret(ctx, "ARGOCD_TOKEN")
//	if err != nil { return err }
func Secret(ctx context.Context, name string) (string, error) {
	v, masked, err := resolveValue(ctx, "Secret", name)
	if err != nil {
		return "", err
	}
	if !masked {
		return "", fmt.Errorf("sparkwing: Secret(%q): entry is stored with masked=false; use sparkwing.Variable", name)
	}
	return v, nil
}

func resolveValue(ctx context.Context, caller, name string) (string, bool, error) {
	if name == "" {
		return "", false, fmt.Errorf("sparkwing: %s: name is required", caller)
	}
	r := secretResolverFromContext(ctx)
	if r == nil {
		return "", false, fmt.Errorf("sparkwing: %s: no resolver installed -- %s can only be called from step bodies or memo key functions, not from Plan() (resolver is attached at dispatch time)", caller, caller)
	}
	v, masked, err := r.resolve(ctx, name)
	if err != nil {
		return "", false, fmt.Errorf("sparkwing: %s(%q): %w", caller, name, err)
	}
	return v, masked, nil
}

// MustSecret is Secret that panics on error. Most call sites should
// prefer Secret + explicit error handling so failures surface as job
// errors rather than panics.
func MustSecret(ctx context.Context, name string) string {
	v, err := Secret(ctx, name)
	if err != nil {
		panic(fmt.Errorf("sparkwing.MustSecret(%q): %w", name, err))
	}
	return v
}
