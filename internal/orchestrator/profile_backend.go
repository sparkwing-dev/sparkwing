package orchestrator

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/discovery"
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

// hack: indirections so a test can count every store a run opens, whichever
// path opened it.
var (
	openStateStoreFromSpec = storeurl.OpenStateStoreFromSpec
	storeOpen              = store.Open
)

// safety: keepState is set by a run whose state already reaches this
// machine's store through the admission daemon or the standalone fallback.
// Opening the state database again would take the file that run does not own.
func applyLocalBackends(ctx context.Context, opts *Options, paths Paths, keepState bool) error {
	if opts.LocalOnly {
		opts.LogStore = nil
		opts.ArtifactStore = nil
		if !keepState {
			opts.State = nil
		}
	}
	if opts.ArtifactStore == nil {
		art, err := localArtifactStore(paths)
		if err != nil {
			return fmt.Errorf("local artifact cache: %w", err)
		}
		opts.ArtifactStore = art
	}
	if keepState || opts.State != nil {
		return nil
	}
	if opts.DefaultStateDB == "" {
		return fmt.Errorf("state backend: no default state database path resolved")
	}
	st, err := openStateStoreFromSpec(ctx, backends.Spec{Type: backends.TypeSQLite, Path: opts.DefaultStateDB}, nil)
	if err != nil {
		return fmt.Errorf("open sqlite state: %w", err)
	}
	opts.State = st
	return nil
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

func localArtifactStore(paths Paths) (storage.ArtifactStore, error) {
	return fsstore.NewArtifactStore(filepath.Join(paths.Root, "cache"))
}
