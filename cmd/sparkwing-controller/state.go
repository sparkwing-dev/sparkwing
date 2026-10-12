package main

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func openControllerStore(ctx context.Context, sqlitePath, pgURL string) (*store.Store, error) {
	if pgURL != "" {
		return store.OpenPostgres(ctx, pgURL)
	}
	return store.Open(sqlitePath) //nolint:contextcheck // Store.Open owns its bounded migration context and has no caller-context variant.
}
