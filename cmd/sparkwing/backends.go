package main

import (
	"log/slog"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

// resolveBinaryCacheSpec resolves the backend that serves pipeline
// binaries under bin/<hash>: the active profile's cache surface, or its
// binaries sub-spec when it declares one.
func resolveBinaryCacheSpec() (*backends.Spec, storeurl.ProfileLookup) {
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
	p, _, err := profile.Resolve(name, cfg)
	if err != nil {
		slog.Default().Debug("profile resolve failed", "err", err)
		return nil, nil
	}
	if p == nil {
		return nil, nil
	}
	lookup := controllerLookup(p)
	if bin := p.Surfaces().BinaryCache(); bin != nil {
		return bin, lookup
	}
	if p.ControllerURL() != "" {
		return &backends.Spec{Type: backends.TypeController, Controller: p.Name}, lookup
	}
	return nil, nil
}

func controllerLookup(p *profile.Profile) storeurl.ProfileLookup {
	if p == nil || p.ControllerURL() == "" {
		return nil
	}
	return func(string) (string, string, error) {
		return p.ControllerURL(), p.ControllerToken(), nil
	}
}
