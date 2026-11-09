package sparkwing

import (
	"context"
	"fmt"

	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/secretvalue"
)

// ErrSecretMissing classifies a missing stored value separately from transport
// and authorization failures. Use errors.Is to recognize a miss from any backend.
var ErrSecretMissing = secretvalue.ErrSecretMissing

// Secrets are declared during describe and resolved lazily during execution.
// The host registers masked values before releasing them. Secret and Variable reject classification drift.
type secretResolver interface {
	resolve(ctx context.Context, name string) (value string, masked bool, err error)
}

type secretResolverFunc func(ctx context.Context, name string) (value string, masked bool, err error)

func (f secretResolverFunc) resolve(ctx context.Context, name string) (string, bool, error) {
	return f(ctx, name)
}

const keySecretResolver ctxKey = 100

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
// Variable for those). Declare the name during describe before resolving it
// from a step body or memo key function.
//
//	token, err := sparkwing.Secret(ctx, "ARGOCD_TOKEN")
//	if err != nil { return err }
func Secret(ctx context.Context, name string) (string, error) {
	v, _, err := resolveValue(ctx, "Secret", name)
	return v, err
}

func resolveValue(ctx context.Context, caller, name string) (string, bool, error) {
	r := secretResolverFromContext(ctx)
	if r == nil {
		return "", false, fmt.Errorf("sparkwing: %s: no resolver installed -- %s can only be called from step bodies or memo key functions, not from Plan() (resolver is attached at dispatch time)", caller, caller)
	}
	v, masked, err := r.resolve(sdkserve.WithSecretClass(ctx, caller == "Variable"), name)
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
