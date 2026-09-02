package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

func effectiveSecretsSpec(opts Options) *backends.Spec {
	if opts.LocalOnly || opts.Profile == nil {
		return nil
	}
	return opts.Profile.Surfaces().Secrets
}

func resolverAsSource(ctx context.Context, r sparkwing.SecretResolver) secrets.Source {
	return secrets.SourceFunc(func(name string) (string, bool, error) {
		return r.Resolve(ctx, name)
	})
}
