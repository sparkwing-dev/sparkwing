package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOpenControllerStoreDefaultsToSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := openControllerStore(context.Background(), path, "")
	if err != nil {
		t.Fatalf("open controller store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if got := st.Dialect(); got != store.DialectSQLite {
		t.Fatalf("dialect = %v, want sqlite", got)
	}
}

func TestOpenControllerStoreUsesThePostgresCredential(t *testing.T) {
	_, err := openControllerStore(context.Background(), filepath.Join(t.TempDir(), "state.db"),
		"postgres://sparkwing@127.0.0.1:1/none?connect_timeout=1&sslmode=disable")
	if err == nil {
		t.Fatal("open controller store reached SQLite although the pg-url credential named PostgreSQL")
	}
}
