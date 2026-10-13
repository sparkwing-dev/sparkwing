package backend

import (
	"context"
	"fmt"
	"io"

	swpaths "github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// FromSpecs opens the read backend a profile's state spec names. A sqlite
// store reads node logs from the run directories under paths; a controller
// serves them from its own log segments.
func FromSpecs(
	ctx context.Context,
	stateSpec *backends.Spec,
	paths swpaths.Paths,
	profileLookup storeurl.ProfileLookup,
) (Backend, io.Closer, error) {
	if stateSpec == nil {
		return nil, nopCloser{}, fmt.Errorf("state spec is required")
	}

	switch stateSpec.Type {
	case backends.TypeSQLite:
		ss, err := storeurl.OpenStateStoreFromSpec(ctx, *stateSpec, profileLookup)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("state backend: %w", err)
		}
		st, ok := ss.(*store.Store)
		if !ok {
			return nil, nopCloser{}, fmt.Errorf("state backend type=%s did not return *store.Store", stateSpec.Type)
		}
		return NewStoreBackend(st, paths, nil), st, nil

	case backends.TypeController:
		ss, err := storeurl.OpenStateStoreFromSpec(ctx, *stateSpec, profileLookup)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("state backend: %w", err)
		}
		c, ok := ss.(*client.Client)
		if !ok {
			return nil, nopCloser{}, fmt.Errorf("state backend type=controller did not return *client.Client")
		}
		return NewClientBackend(c, c.NodeLogs()), c, nil

	default:
		return nil, nopCloser{}, fmt.Errorf("state backend type %q not supported by the dashboard", stateSpec.Type)
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
