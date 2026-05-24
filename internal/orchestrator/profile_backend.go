package orchestrator

import (
	"context"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

// OpenReadBackendForProfile opens the dashboard-shaped read backend from
// a resolved profile's surfaces. It is the profile-driven sibling of
// OpenReadBackend: callers that resolve a profile first (via
// internal/profile.ResolveChain) ask for the read surface here instead
// of walking cwd for a backends.yaml. The state surface defaults to
// SQLite at paths.StateDB() when the profile declares none; a profile
// carrying only controller: routes every surface through that
// controller. Callers MUST defer the returned Closer.
func OpenReadBackendForProfile(ctx context.Context, paths Paths, p *profile.Profile) (backend.Backend, io.Closer, error) {
	state, logs, cache := profileSurfaceSpecs(p, paths.StateDB())
	return backend.FromSpecs(ctx, state, logs, cache, paths, profileControllerLookup(p))
}

// ApplyProfileBackends populates opts.State / opts.LogStore /
// opts.ArtifactStore from a resolved profile's surfaces. It mirrors
// ApplyBackendsConfig's effect but sources surfaces from the profile
// resolver instead of backends.yaml. opts.LocalOnly still short-circuits
// to the SQLite-only path, and values the caller pre-set are preserved.
func ApplyProfileBackends(ctx context.Context, opts *Options, p *profile.Profile) error {
	if opts.LocalOnly {
		opts.LogStore = nil
		opts.ArtifactStore = nil
		opts.State = nil
		if opts.DefaultStateDB == "" {
			return fmt.Errorf("--sw-local-only: no default state database path resolved")
		}
		spec := backends.Spec{Type: backends.TypeSQLite, Path: opts.DefaultStateDB}
		st, err := storeurl.OpenStateStoreFromSpec(ctx, spec, nil)
		if err != nil {
			return fmt.Errorf("--sw-local-only: open sqlite state: %w", err)
		}
		opts.State = st
		return nil
	}

	state, logs, cache := profileSurfaceSpecs(p, opts.DefaultStateDB)

	lookup := profileControllerLookup(p)
	if l := storeurlProfileLookup(opts.ProfileLookup); l != nil {
		lookup = l
	}

	if opts.ArtifactStore == nil && cache != nil {
		store, err := storeurl.OpenArtifactStoreFromSpec(ctx, *cache, lookup)
		if err != nil {
			return fmt.Errorf("cache backend: %w", err)
		}
		opts.ArtifactStore = store
	}
	if opts.LogStore == nil && logs != nil {
		store, err := storeurl.OpenLogStoreFromSpec(ctx, *logs, lookup)
		if err != nil {
			return fmt.Errorf("logs backend: %w", err)
		}
		opts.LogStore = store
	}
	if opts.State == nil && state != nil {
		st, err := storeurl.OpenStateStoreFromSpec(ctx, *state, lookup)
		if err != nil {
			return fmt.Errorf("state backend: %w", err)
		}
		opts.State = st
	}
	return nil
}

// profileSurfaceSpecs derives the state/logs/cache specs for opening
// backends from a resolved profile. Three shapes:
//
//   - explicit surfaces (any of state/cache/logs set) → use them as-is;
//     a sqlite state surface without a path is filled with stateDBPath.
//   - controller-only (no surfaces but controller: set) → every surface
//     resolves through the controller named by the profile.
//   - bare (neither) → sqlite state at stateDBPath, no shared logs or
//     cache (the historical local default).
//
// The profile's own spec pointers are never mutated; the sqlite path
// default is applied to a clone.
func profileSurfaceSpecs(p *profile.Profile, stateDBPath string) (state, logs, cache *backends.Spec) {
	surf := p.Surfaces()
	if surf.State == nil && surf.Logs == nil && surf.Cache == nil {
		if p != nil && p.Controller != "" {
			ctrl := func() *backends.Spec { return &backends.Spec{Type: backends.TypeController, Controller: p.Name} }
			return ctrl(), ctrl(), ctrl()
		}
		return &backends.Spec{Type: backends.TypeSQLite, Path: stateDBPath}, nil, nil
	}

	state = surf.State
	switch {
	case state == nil:
		state = &backends.Spec{Type: backends.TypeSQLite, Path: stateDBPath}
	case state.Type == backends.TypeSQLite && state.Path == "":
		filled := *state
		filled.Path = stateDBPath
		state = &filled
	}
	return state, surf.Logs, surf.Cache
}

// profileControllerLookup builds a storeurl.ProfileLookup that resolves
// any controller-typed spec to this profile's controller URL and token.
// Returns nil when the profile declares no controller, so the factories
// give their usual "no lookup provided" error if a controller spec
// nonetheless appears.
func profileControllerLookup(p *profile.Profile) storeurl.ProfileLookup {
	if p == nil || p.Controller == "" {
		return nil
	}
	return func(string) (string, string, error) {
		return p.Controller, p.Token, nil
	}
}
