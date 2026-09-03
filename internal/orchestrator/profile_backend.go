package orchestrator

import (
	"context"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func OpenReadBackendForProfile(ctx context.Context, paths Paths, p *profile.Profile) (backend.Backend, io.Closer, error) {
	state, logs, cache := profileSurfaceSpecs(p, paths.StateDB())
	return backend.FromSpecs(ctx, state, logs, cache, paths, profileControllerLookup(p))
}

func ApplyProfileBackends(ctx context.Context, opts *Options, p *profile.Profile) error {
	return applyProfileBackends(ctx, opts, p, false)
}

// safety: keepState is set by a run whose state already reaches this
// machine's store through the admission daemon. Resolving the state surface
// again would open the file the daemon owns, which is the whole point of
// that run not owning it.
func applyProfileBackends(ctx context.Context, opts *Options, p *profile.Profile, keepState bool) error {
	if opts.LocalOnly {
		opts.LogStore = nil
		opts.ArtifactStore = nil
		if keepState {
			return nil
		}
		opts.State = nil
		if opts.DefaultStateDB == "" {
			return fmt.Errorf("--sw-local-only: no default state database path resolved")
		}
		spec := backends.Spec{Type: backends.TypeSQLite, Path: opts.DefaultStateDB}
		st, err := openStateStoreFromSpec(ctx, spec, nil)
		if err != nil {
			return fmt.Errorf("--sw-local-only: open sqlite state: %w", err)
		}
		opts.State = st
		return nil
	}

	state, logs, cache := effectiveSurfaceSpecs(p, opts, opts.DefaultStateDB)

	lookup := profileControllerLookup(p)
	if opts.ProfileLookup != nil {
		lookup = opts.ProfileLookup
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
	if opts.State == nil && state != nil && !keepState {
		st, err := openStateStoreFromSpec(ctx, *state, lookup)
		if err != nil {
			return fmt.Errorf("state backend: %w", err)
		}
		opts.State = st
	}
	return nil
}

// openStateStoreFromSpec is the orchestrator's only path to a state store
// file, so a test counts what a run opens by replacing it.
var openStateStoreFromSpec = storeurl.OpenStateStoreFromSpec

func ApplyProfileBackendsWithMirror(ctx context.Context, opts *Options, p *profile.Profile, paths Paths) error {
	return applyProfileBackendsWithMirror(ctx, opts, p, paths, false)
}

func applyProfileBackendsWithMirror(ctx context.Context, opts *Options, p *profile.Profile, paths Paths, keepState bool) error {
	hadState := opts.State != nil
	if err := applyProfileBackends(ctx, opts, p, keepState); err != nil {
		return err
	}
	if opts.LocalOnly || hadState || opts.State == nil {
		return nil
	}
	if p == nil || !p.EffectiveMirrorLocal() {
		return nil
	}
	state, _, _ := profileSurfaceSpecs(p, opts.DefaultStateDB)
	if isLocalState(state) {
		return nil
	}
	local, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("mirror: open local state %s: %w", paths.StateDB(), err)
	}
	opts.MirrorLocal = local
	return nil
}

func isLocalState(spec *backends.Spec) bool {
	return spec == nil || spec.Type == backends.TypeSQLite
}

func effectiveSurfaceSpecs(p *profile.Profile, _ *Options, stateDBPath string) (state, logs, cache *backends.Spec) {
	if p != nil {
		return profileSurfaceSpecs(p, stateDBPath)
	}
	return &backends.Spec{Type: backends.TypeSQLite, Path: stateDBPath}, nil, nil
}

func profileSurfaceSpecs(p *profile.Profile, stateDBPath string) (state, logs, cache *backends.Spec) {
	surf := p.Surfaces()
	if surf.State == nil && surf.Logs == nil && surf.Cache == nil {
		if p != nil && p.ControllerURL() != "" {
			ctrl := func() *backends.Spec { return &backends.Spec{Type: backends.TypeController, Controller: p.Name} }

			logsSpec := ctrl()
			logsSpec.URL = announcedLogsURL(p)
			return ctrl(), logsSpec, ctrl()
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

func announcedLogsURL(p *profile.Profile) string {
	if p == nil || p.ControllerURL() == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), logsDiscoveryTimeout)
	defer cancel()
	svc, err := discovery.ServicesFor(ctx, p.ControllerURL(), p.ControllerToken())
	if err != nil {
		return ""
	}
	return svc.Logs
}

func profileControllerLookup(p *profile.Profile) storeurl.ProfileLookup {
	if p == nil || p.ControllerURL() == "" {
		return nil
	}
	return func(string) (string, string, error) {
		return p.ControllerURL(), p.ControllerToken(), nil
	}
}
