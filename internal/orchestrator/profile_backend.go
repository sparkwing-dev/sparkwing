package orchestrator

import (
	"context"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func OpenReadBackendForProfile(ctx context.Context, paths Paths, p *profile.Profile) (backend.Backend, io.Closer, error) {
	return backend.FromSpecs(ctx, profileStateSpec(p, paths.StateDB()), paths, profileControllerLookup(p))
}

func profileStateSpec(p *profile.Profile, stateDBPath string) *backends.Spec {
	state := p.Surfaces().State
	switch {
	case state == nil && p != nil && p.ControllerURL() != "":
		return &backends.Spec{Type: backends.TypeController, Controller: p.Name}
	case state == nil:
		return &backends.Spec{Type: backends.TypeSQLite, Path: stateDBPath}
	case state.Type == backends.TypeSQLite && state.Path == "":
		filled := *state
		filled.Path = stateDBPath
		return &filled
	}
	return state
}

func profileControllerLookup(p *profile.Profile) storeurl.ProfileLookup {
	if p == nil || p.ControllerURL() == "" {
		return nil
	}
	return func(string) (string, string, error) {
		return p.ControllerURL(), p.ControllerToken(), nil
	}
}
