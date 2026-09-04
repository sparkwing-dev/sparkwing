package store_test

import (
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMigrate_ConcurrentColdStartConverges(t *testing.T) {
	target := storetest.New(t)

	const openers = 8
	errs := make([]error, openers)
	var wg sync.WaitGroup
	for i := range openers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := target.TryOpen()
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

	s, err := target.TryOpen()
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
