package main

import (
	"log/slog"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

// resolveEffectiveCacheSpec returns the cache backend spec (and the
// controller lookup needed to open it) the sparkwing CLI should consult
// before the orchestrator boots, so the compile step can fetch a
// pre-built pipeline binary from a shared artifact store.
//
// The spec comes from the active profile's cache surface: an explicit
// cache backend when the profile declares one, the controller when the
// profile is controller-only, or nil for a bare/laptop profile (the
// compile loop then falls through to the gitcache / local-build paths).
// Resolution is best-effort -- the flag level (SPARKWING_PROFILE) plus
// profiles.yaml default; failures yield (nil, nil).
func resolveEffectiveCacheSpec(_ string) (*backends.Spec, storeurl.ProfileLookup) {
	name := os.Getenv("SPARKWING_PROFILE")
	path, err := profile.DefaultPath()
	if err != nil {
		slog.Default().Debug("profiles.yaml path resolve failed", "err", err)
		return nil, nil
	}
	cfg, err := profile.Load(path)
	if err != nil {
		slog.Default().Debug("profiles.yaml load failed", "err", err)
		return nil, nil
	}
	p, _, err := profile.Resolve(name, "", cfg)
	if err != nil {
		slog.Default().Debug("profile resolve failed", "err", err)
		return nil, nil
	}
	lookup := controllerLookup(p)
	if cache := p.Surfaces().Cache; cache != nil {
		return cache, lookup
	}
	if p.Controller != "" {
		return &backends.Spec{Type: backends.TypeController, Controller: p.Name}, lookup
	}
	return nil, nil
}

// controllerLookup adapts a profile's controller/token to the storeurl
// factory lookup, so a controller-typed cache spec resolves. Returns nil
// when the profile declares no controller.
func controllerLookup(p *profile.Profile) storeurl.ProfileLookup {
	if p == nil || p.Controller == "" {
		return nil
	}
	return func(string) (string, string, error) {
		return p.Controller, p.Token, nil
	}
}
