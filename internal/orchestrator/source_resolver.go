package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// selectSecretResolver picks the secrets.Source for this run from the
// effective secrets backend. With an active profile (--profile X), the
// profile's Surfaces.Secrets wins outright; without one, the project
// default's secrets surface is used.
//
// Returns (nil, nil) when no secrets backend is declared at either
// layer (the caller leaves any existing Options.SecretSource path
// untouched).
//
// The returned source is uncached; the caller wraps it in
// secrets.NewCached + masker before installing on ctx.
func selectSecretResolver(ctx context.Context, opts Options) (secrets.Source, error) {
	spec := effectiveSecretsSpec(opts)
	if spec == nil {
		return nil, nil
	}
	resolver, err := sparkwing.NewSecretResolverFromSpec(ctx, *spec)
	if err != nil {
		return nil, err
	}
	return resolverAsSource(ctx, resolver), nil
}

// effectiveSecretsSpec returns the secrets backend that applies to
// this run. Profile wins wholesale when active.
func effectiveSecretsSpec(opts Options) *backends.Spec {
	if opts.Profile != nil && opts.Profile.Surfaces().Secrets != nil {
		return opts.Profile.Surfaces().Secrets
	}
	if opts.ProjectBackends.Secrets != nil {
		return opts.ProjectBackends.Secrets
	}
	return nil
}

// resolverAsSource adapts a sparkwing.SecretResolver to the
// secrets.Source contract so it composes with secrets.NewCached.
func resolverAsSource(ctx context.Context, r sparkwing.SecretResolver) secrets.Source {
	return secrets.SourceFunc(func(name string) (string, bool, error) {
		return r.Resolve(ctx, name)
	})
}
