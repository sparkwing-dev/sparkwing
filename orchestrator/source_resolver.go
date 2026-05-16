package orchestrator

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/sources"
	"github.com/sparkwing-dev/sparkwing/profile"
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// selectSecretResolver picks the secrets.Source for this run based on
// the pipelines.yaml target's source binding (when available),
// falling back to the sources.yaml default, and ultimately leaving
// the existing Options.SecretSource path untouched. Returns
// (resolver, nil) when a source binding produced one, (nil, nil)
// when nothing in sources.yaml applies, and (_, err) when a binding
// was attempted but failed (unknown source name, profile lookup
// failure, type-required-field missing, etc.).
//
// The returned source is uncached; the caller wraps it in
// secrets.NewCached + masker before installing on ctx.
func selectSecretResolver(ctx context.Context, opts Options) (secrets.Source, error) {
	if opts.SparkwingDir == "" {
		return nil, nil
	}
	srcName := ""
	if opts.PipelineYAML != nil && opts.Target != "" {
		if t, ok := opts.PipelineYAML.Targets[opts.Target]; ok {
			srcName = t.Source
		}
	}
	src, ok, err := sources.Resolve(opts.SparkwingDir, srcName)
	if err != nil {
		return nil, fmt.Errorf("source binding: %w", err)
	}
	if !ok {
		return nil, nil
	}
	resolver, err := sparkwing.NewSecretResolverFromSource(ctx, src, profileLookupCallback())
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", src.Name, err)
	}
	return resolverAsSource(ctx, resolver), nil
}

// resolverAsSource adapts a sparkwing.SecretResolver to the
// secrets.Source contract so it composes with secrets.NewCached.
func resolverAsSource(ctx context.Context, r sparkwing.SecretResolver) secrets.Source {
	return secrets.SourceFunc(func(name string) (string, bool, error) {
		return r.Resolve(ctx, name)
	})
}

// profileLookupCallback returns the closure the SDK factory calls
// for remote-controller sources. The orchestrator already imports
// the profile package, so the lookup goes through profile.Load +
// profile.Resolve directly.
func profileLookupCallback() sparkwing.ProfileLookup {
	return func(name string) (string, string, error) {
		path, err := profile.DefaultPath()
		if err != nil {
			return "", "", err
		}
		cfg, err := profile.Load(path)
		if err != nil {
			return "", "", err
		}
		p, err := profile.Resolve(cfg, name)
		if err != nil {
			return "", "", err
		}
		if p.Controller == "" {
			return "", "", fmt.Errorf("profile %q has no controller URL", p.Name)
		}
		return p.Controller, p.Token, nil
	}
}
