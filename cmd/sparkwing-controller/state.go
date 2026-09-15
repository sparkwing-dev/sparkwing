package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const controllerPostgresEnv = "SPARKWING_PG_URL"

func openControllerStore(ctx context.Context, sqlitePath string) (*store.Store, error) {
	spec := backends.Spec{Type: backends.TypeSQLite, Path: sqlitePath}
	if _, configured := os.LookupEnv(controllerPostgresEnv); configured {
		spec = backends.Spec{
			Type:      backends.TypePostgres,
			URLSource: "env:" + controllerPostgresEnv,
		}
	}

	state, err := storeurl.OpenStateStoreFromSpec(ctx, spec, nil)
	if err != nil {
		return nil, err
	}
	st, ok := state.(*store.Store)
	if !ok {
		_ = state.Close()
		return nil, fmt.Errorf("controller state backend type=%s did not return *store.Store", spec.Type)
	}
	return st, nil
}
