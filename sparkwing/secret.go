package sparkwing

import (
	"context"
	"errors"
	"fmt"
)

// ErrSecretMissing classifies a "no entry for this name" outcome from
// a SecretResolver as distinct from a transport / authorization error.
// SDK helpers like ResolvePipelineSecrets treat optional fields whose
// resolver error matches this sentinel as silently empty; other
// errors surface as run-start failures.
//
// The canonical instance lives here; the secrets package re-exports
// the same value so existing callers that say
// `errors.Is(err, secrets.ErrSecretMissing)` keep working unchanged.
var ErrSecretMissing = errors.New("sparkwing: secret not found")

// SecretResolver resolves a stored value to (plain, masked) at the
// moment of the call. The orchestrator installs a resolver on the
// run ctx; jobs read values through the package-level Secret / Config
// helpers.
//
// Resolution is on-demand: no plan-time declaration, no dispatch-time
// pre-injection. Implementations are expected to cache successful
// results per-run and register masked values with the run's log masker.
//
// The masked return drives strict matching at the SDK call site:
// Secret errors on masked=false entries, Config errors on masked=true
// ones, so classification drift surfaces loudly.
type SecretResolver interface {
	Resolve(ctx context.Context, name string) (value string, masked bool, err error)
}

// SecretResolverFunc adapts a function to SecretResolver.
type SecretResolverFunc func(ctx context.Context, name string) (value string, masked bool, err error)

// Resolve satisfies SecretResolver.
func (f SecretResolverFunc) Resolve(ctx context.Context, name string) (string, bool, error) {
	return f(ctx, name)
}

const keySecretResolver ctxKey = 100

// WithSecretResolver returns a derived ctx carrying the given resolver.
// The orchestrator installs one per run; jobs reach it via Secret.
func WithSecretResolver(ctx context.Context, r SecretResolver) context.Context {
	return context.WithValue(ctx, keySecretResolver, r)
}

func secretResolverFromContext(ctx context.Context) SecretResolver {
	if r, ok := ctx.Value(keySecretResolver).(SecretResolver); ok {
		return r
	}
	return nil
}

// Secret resolves a masked value through the resolver installed on
// ctx. Errors when no resolver is installed, the source can't produce
// a value, or the entry exists but was stored with masked=false (use
// Config for those). Lazy by design: a misspelled name fails at the
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
		return "", fmt.Errorf("sparkwing: Secret(%q): entry is stored with masked=false; use sparkwing.Config", name)
	}
	return v, nil
}

// Config resolves a non-secret config value through the same store
// as Secret. Errors when the entry was stored with masked=true so a
// caller asking for "config" doesn't accidentally read a secret and
// log it raw.
//
//	region := sparkwing.Config(ctx, "REGION")
func Config(ctx context.Context, name string) (string, error) {
	v, masked, err := resolveValue(ctx, "Config", name)
	if err != nil {
		return "", err
	}
	if masked {
		return "", fmt.Errorf("sparkwing: Config(%q): entry is stored with masked=true; use sparkwing.Secret", name)
	}
	return v, nil
}

func resolveValue(ctx context.Context, caller, name string) (string, bool, error) {
	if name == "" {
		return "", false, fmt.Errorf("sparkwing: %s: name is required", caller)
	}
	r := secretResolverFromContext(ctx)
	if r == nil {
		return "", false, fmt.Errorf("sparkwing: %s: no resolver installed -- %s can only be called from step bodies or CacheKey functions, not from Plan() (resolver is attached at dispatch time)", caller, caller)
	}
	v, masked, err := r.Resolve(ctx, name)
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

// MustConfig is Config that panics on error. Same trade-off as
// MustSecret: prefer the error-returning form unless a missing value
// is genuinely a programmer mistake.
func MustConfig(ctx context.Context, name string) string {
	v, err := Config(ctx, name)
	if err != nil {
		panic(fmt.Errorf("sparkwing.MustConfig(%q): %w", name, err))
	}
	return v
}
