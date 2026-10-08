package main

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func openControllerStore(ctx context.Context, sqlitePath, pgURL string) (*store.Store, error) {
	spec := backends.Spec{Type: backends.TypeSQLite, Path: sqlitePath}
	if pgURL != "" {
		spec = backends.Spec{Type: backends.TypePostgres, URL: pgURL}
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
