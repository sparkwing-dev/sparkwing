package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// OpenReadBackend opens the dashboard-shaped read backend the local
// CLI's `runs list` / `runs status` / `runs logs` commands should
// consult. It mirrors `sparkwing run`'s resolution rules so a single
// .sparkwing/backends.yaml describes both write and read paths:
//
//  1. Discover .sparkwing/ from cwd (or honor SPARKWING_DIR if set).
//  2. Resolve backends.yaml (repo + user + built-in environments).
//  3. Auto-detect the active environment (gha, kubernetes, etc.) and
//     pick the effective state/logs/cache specs.
//  4. Dispatch through [backend.FromSpecs] to the right impl --
//     StoreBackend over local SQLite, S3Backend over the bucket, or
//     ClientBackend over the configured controller URL.
//
// When no backends.yaml is found AND no environment auto-detects,
// falls back to the historical SQLite-at-paths.StateDB() default so
// pre-Mode-2 setups keep working with zero config. Callers MUST defer
// the returned Closer.
//
// Until this helper existed, every read command opened
// store.Open(paths.StateDB()) directly, silently ignoring an S3 or
// Postgres backends.yaml and reading from a phantom local DB that the
// orchestrator never wrote to. Mode 2 users saw "no runs yet" against
// a bucket full of runs.
func OpenReadBackend(ctx context.Context, paths Paths) (backend.Backend, io.Closer, error) {
	sparkwingDir := discoverSparkwingDir()

	file, err := backends.Resolve(sparkwingDir)
	if err != nil {
		return nil, nopCloser{}, fmt.Errorf("backends.yaml: %w", err)
	}
	envName, _, _ := backends.DetectEnvironment(file)
	eff := backends.Effective(file, envName, backends.Surfaces{})

	state := eff.State
	if state == nil {
		state = &backends.Spec{Type: backends.TypeSQLite, Path: paths.StateDB()}
	}
	if state.Type == backends.TypeSQLite && state.Path == "" {
		state.Path = paths.StateDB()
	}
	return backend.FromSpecs(ctx, state, eff.Logs, eff.Cache, paths, nil)
}

// discoverSparkwingDir walks up from cwd looking for a .sparkwing
// directory containing pipelines.yaml. Returns "" when none is found
// so Resolve falls back to user-level + built-in configuration.
func discoverSparkwingDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if yamlPath, _, err := pipelines.Discover(cwd); err == nil && yamlPath != "" {
		return filepathDir(yamlPath)
	}
	return ""
}

// filepathDir is filepath.Dir without an extra import; the function
// is one line so keeping the package list trimmed isn't worth a new
// import line.
func filepathDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
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
