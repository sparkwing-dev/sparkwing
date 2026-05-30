package orchestrator

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/sources"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// selectSecretResolver picks the secrets.Source for this run.
// Precedence: profile.SourceOverride wins outright; otherwise the
// pipeline's inline dispatch.source is used. Returns (nil, nil) when
// neither path produces a source binding (the caller leaves any
// existing Options.SecretSource path untouched).
//
// For type=controller sources, the source's URL must match the
// active profile's controller URL; on a mismatch this returns a
// clear error so the run fails before any pod spins up. The
// profile's controller token is passed through.
//
// The returned source is uncached; the caller wraps it in
// secrets.NewCached + masker before installing on ctx.
func selectSecretResolver(ctx context.Context, opts Options) (secrets.Source, error) {
	src := pickActiveSource(opts.Profile, opts.PipelineYAML)
	if src == nil {
		return nil, nil
	}
	token, err := controllerTokenFor(*src, opts.Profile)
	if err != nil {
		return nil, err
	}
	resolver, err := sparkwing.NewSecretResolverFromSource(ctx, *src, token)
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", src.Describe(), err)
	}
	return resolverAsSource(ctx, resolver), nil
}

// pickActiveSource applies the SourceOverride-wins-over-pipeline
// precedence. Returns nil when neither layer declares one.
func pickActiveSource(p *profile.Profile, py *pipelines.Pipeline) *sources.Source {
	if p != nil && p.SourceOverride != nil {
		return p.SourceOverride
	}
	if py == nil || py.Dispatch == nil {
		return nil
	}
	return py.Dispatch.Source
}

// controllerTokenFor enforces the URL-match policy for
// type=controller sources and returns the profile's controller token.
// For other source types it returns "" -- file/env don't use it.
func controllerTokenFor(src sources.Source, p *profile.Profile) (string, error) {
	if src.Type != sources.TypeController {
		return "", nil
	}
	if p == nil || p.ControllerURL() == "" {
		return "", fmt.Errorf("source %s: type=%s requires an active profile with a controller; none is configured",
			src.Describe(), src.Type)
	}
	if src.URL != p.ControllerURL() {
		return "", fmt.Errorf("source %s: pipeline expects controller %s but active profile %q points at %s",
			src.Describe(), src.URL, p.Name, p.ControllerURL())
	}
	return p.ControllerToken(), nil
}

// resolverAsSource adapts a sparkwing.SecretResolver to the
// secrets.Source contract so it composes with secrets.NewCached.
func resolverAsSource(ctx context.Context, r sparkwing.SecretResolver) secrets.Source {
	return secrets.SourceFunc(func(name string) (string, bool, error) {
		return r.Resolve(ctx, name)
	})
}
