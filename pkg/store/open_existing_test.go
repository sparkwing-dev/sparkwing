package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// Break-glass must never act on a store it just created, so a missing or
// empty target is refused and left as it was.
func TestOpenExistingNeverCreatesASQLiteStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if _, err := store.OpenExisting(t.Context(), store.DialectSQLite, path); err == nil {
		t.Fatal("OpenExisting opened a missing store")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenExisting created %s: %v", path, err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenExisting(t.Context(), store.DialectSQLite, path); err == nil {
		t.Fatal("OpenExisting accepted an empty file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	created, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenExisting(t.Context(), store.DialectSQLite, path)
	if err != nil {
		t.Fatalf("OpenExisting on a created store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenExistingNeverCreatesAPostgresStore(t *testing.T) {
	target := storetest.NewPostgres(t)
	if _, err := store.OpenExisting(t.Context(), store.DialectPostgres, target.DSN()); err == nil {
		t.Fatal("OpenExisting opened an empty database")
	}
	target.Open(t)
	st, err := store.OpenExisting(t.Context(), store.DialectPostgres, target.DSN())
	if err != nil {
		t.Fatalf("OpenExisting on a created store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}
