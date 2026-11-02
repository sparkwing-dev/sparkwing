package teststore_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestExecKeepsConnectionStateAndPragmas(t *testing.T) {
	st, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := teststore.Exec(ctx, st, `CREATE TEMP TABLE connection_state (id INTEGER PRIMARY KEY, foreign_keys INTEGER CHECK(foreign_keys=1), timeout INTEGER CHECK(timeout=30000))`); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for id := range 8 {
		workers.Go(func() {
			if _, err := teststore.Exec(ctx, st, `INSERT INTO connection_state SELECT ?,foreign_keys,timeout FROM pragma_foreign_keys(),pragma_busy_timeout()`, id); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if _, err := teststore.Exec(ctx, st, `INSERT INTO settings(key,value) VALUES ('connection_count',CAST((SELECT COUNT(*) FROM connection_state) AS BLOB))`); err != nil {
		t.Fatal(err)
	}
	var count string
	if err := teststore.SQL(t, st).QueryRowContext(ctx, `SELECT value FROM settings WHERE key='connection_count'`).Scan(&count); err != nil || count != "8" {
		t.Fatalf("connection state count = %q, %v", count, err)
	}
	if _, err := teststore.Exec(ctx, st, `INSERT INTO team_first_success(team,run_id) VALUES ('missing','missing')`); err == nil {
		t.Fatal("fixture accepted missing foreign keys")
	}
}

func TestExecCleanupForgetsStore(t *testing.T) {
	var st *store.Store
	t.Run("fixture", func(t *testing.T) {
		var err error
		st, err = teststore.Open(t, filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if _, err := teststore.Exec(t.Context(), st, `CREATE TEMP TABLE owned_by_test (id INTEGER)`); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := teststore.Exec(context.Background(), st, `SELECT 1`); err == nil {
		t.Fatal("fixture connection outlived its test")
	}
}
