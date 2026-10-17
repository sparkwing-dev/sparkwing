package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func openControllerStore(ctx context.Context, sqlitePath, pgURL string) (*store.Store, error) {
	if pgURL != "" {
		return store.OpenPostgres(ctx, pgURL)
	}
	return store.Open(sqlitePath) //nolint:contextcheck // Store.Open owns its bounded migration context and has no caller-context variant.
}

func newControllerServer(st *store.Store, root, version string) (*controller.Server, error) {
	engines, err := engine.NewResolver(engine.Config{BuildVersion: version, CacheDirectory: filepath.Join(root, "engines")})
	if err != nil {
		return nil, fmt.Errorf("engine version policy: %w", err)
	}
	return controller.New(st, nil).WithEngineController(&controller.EngineController{
		Store:         st,
		RunnerPolicy:  &controller.SignedRunnerAdmission{Resolver: engines},
		VersionPolicy: &controller.SignedEngineVersionPolicy{Resolver: engines},
	}), nil
}
