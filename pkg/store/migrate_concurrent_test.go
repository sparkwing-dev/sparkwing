package store_test

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Several processes can cold-start the same fresh state.db at once --
// Box-scoped budgeting actively encourages concurrent `sparkwing run`
// on one host. The first writer's WAL switch + table creation must not
// fail a concurrent opener with SQLITE_BUSY; every opener should
// converge on the migrated schema.
func TestMigrate_ConcurrentColdStartConverges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	const openers = 8
	errs := make([]error, openers)
	var wg sync.WaitGroup
	for i := range openers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := store.Open(path)
			if err != nil {
				errs[i] = err
				return
			}
			_ = s.Close()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("opener %d failed to cold-start: %v", i, err)
		}
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen after concurrent cold start: %v", err)
	}
	defer func() { _ = s.Close() }()
	var version int
	if err := s.DB().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", version, store.ExpectedSchemaVersion())
	}
}
