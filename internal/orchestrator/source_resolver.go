package orchestrator

import (
	"context"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func selectSecretResolver(ctx context.Context, opts Options) (secrets.Source, error) {
	spec := effectiveSecretsSpec(opts)
	if spec == nil {
		return nil, nil
	}
	if spec.Type == backends.TypeController && spec.URL != "" && opts.RunID != "" {
		c := client.NewWithToken(strings.TrimRight(spec.URL, "/"), http.DefaultClient, spec.ResolvedToken())
		return controllerSecretSource(ctx, c, opts.RunID), nil
	}
	resolver, err := secretsource.FromSpec(*spec)
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

func resolverAsSource(ctx context.Context, r secretsource.Resolver) secrets.Source {
	return secrets.SourceFunc(func(name string) (string, bool, error) {
		return r.Resolve(ctx, name)
	})
}
