package orchestrator

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	fsstore "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func OpenReadBackendForProfile(ctx context.Context, paths Paths, p *profile.Profile) (backend.Backend, io.Closer, error) {
	state, logs, cache := profileSurfaceSpecs(p, paths.StateDB())
	return backend.FromSpecs(ctx, state, logs, cache, paths, profileControllerLookup(p))
}

// safety: keepState is set by a run whose state already reaches this
// machine's store through the admission daemon, or that holds the standalone
// store the fallback chose. Opening the state again would take a file that
// run does not own.
func resolveLocalBackends(opts *Options, paths Paths, keepState bool) error {
	if opts.State == nil && !keepState {
		if opts.DefaultStateDB == "" {
			return fmt.Errorf("state backend: no state database path resolved")
		}
		st, err := storeOpen(opts.DefaultStateDB)
		if err != nil {
			return fmt.Errorf("state backend: open %s: %w", opts.DefaultStateDB, err)
		}
		opts.State = st
	}
	if opts.State == nil {
		return fmt.Errorf("state backend: no store resolved")
	}
	if opts.ArtifactStore == nil {
		art, err := localArtifactStore(paths)
		if err != nil {
			return fmt.Errorf("local artifact cache: %w", err)
		}
		opts.ArtifactStore = art
	}
	return nil
}

// hack: an indirection so a test can count every store a run opens, whichever
// path opened it.
var storeOpen = store.Open

func profileSurfaceSpecs(p *profile.Profile, stateDBPath string) (state, logs, cache *backends.Spec) {
	surf := p.Surfaces()
	if surf.State == nil && surf.Logs == nil && surf.Cache == nil {
		if p != nil && p.ControllerURL() != "" {
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

func profileControllerLookup(p *profile.Profile) storeurl.ProfileLookup {
	if p == nil || p.ControllerURL() == "" {
		return nil
	}
	return func(string) (string, string, error) {
		return p.ControllerURL(), p.ControllerToken(), nil
	}
}

func localArtifactStore(paths Paths) (storage.ArtifactStore, error) {
	return fsstore.NewArtifactStore(filepath.Join(paths.Root, "cache"))
}
