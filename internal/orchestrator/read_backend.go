package orchestrator

import (
	"context"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// OpenReadBackend opens the dashboard-shaped read backend the local
// CLI's `runs list` / `runs status` / `runs logs` commands consult.
// With no --profile active, it opens against the project's local
// SQLite store (no project default backends apply at the read path
// today). Callers MUST defer the returned Closer.
func OpenReadBackend(ctx context.Context, paths Paths) (backend.Backend, io.Closer, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, nopCloser{}, err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, nopCloser{}, fmt.Errorf("profiles.yaml: %w", err)
	}
	p, _, err := profile.Resolve("", cfg)
	if err != nil {
		return nil, nopCloser{}, err
	}
	return OpenReadBackendForProfile(ctx, paths, p)
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// localStore unwraps a backend.Backend to its underlying *store.Store
// when the backend is the local SQLite-backed implementation. Returns
// nil for S3 / controller / Postgres backends. Read commands use this
// to gate sqlite-specific niceties (orphan reconciliation, in-process
// schema introspection) without breaking on other backends.
func localStore(b backend.Backend) *store.Store {
	if sb, ok := b.(*backend.StoreBackend); ok {
		return sb.Store()
	}
	return nil
}
