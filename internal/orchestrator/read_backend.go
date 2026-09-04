package orchestrator

import (
	"context"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func OpenReadBackend(ctx context.Context, paths Paths) (backend.Backend, io.Closer, error) {
	p, err := defaultProfile()
	if err != nil {
		return nil, nopCloser{}, err
	}
	return OpenReadBackendForProfile(ctx, paths, p)
}

func defaultProfile() (*profile.Profile, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, fmt.Errorf("profiles.yaml: %w", err)
	}
	p, _, err := profile.Resolve("", cfg)
	if err != nil {
		return nil, err
	}
	return p, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func localStore(b backend.Backend) *store.Store {
	if sb, ok := b.(*backend.StoreBackend); ok {
		return sb.Store()
	}
	return nil
}

// safety: a profile that declares a logs surface serves log bodies through
// the backend, so the readers that open files under Paths would find nothing.
func logsUnderPaths(ctx context.Context, b backend.Backend) bool {
	caps, err := b.Capabilities(ctx)
	if err != nil {
		return false
	}
	return caps.Storage.Logs == "" || caps.Storage.Logs == "fs"
}
