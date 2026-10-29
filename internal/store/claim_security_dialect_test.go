package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestClaimSecurityRegressions(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 5.8s measured in the short class; the full suite runs it")
	}
	store.ClaimSecurityRegressions(t, func(t *testing.T) *store.Store { return storetest.Open(t) })
}

func TestPostgresClaimSecurityRegressions(t *testing.T) {
	store.ClaimSecurityRegressions(t, func(t *testing.T) *store.Store { return storetest.OpenPostgres(t) })
}
