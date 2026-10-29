package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestRetryPlacementRegressions(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.0s measured in the short class; the full suite runs it")
	}
	store.RetryPlacementRegressions(t, func(t *testing.T) *store.Store { return storetest.Open(t) })
}

func TestPostgresRetryPlacementRegressions(t *testing.T) {
	store.RetryPlacementRegressions(t, func(t *testing.T) *store.Store { return storetest.OpenPostgres(t) })
}
