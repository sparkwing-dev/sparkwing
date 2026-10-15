package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestBilling(t *testing.T) {
	t.Run("billing-start-award-versus-start/store-sqlite", func(t *testing.T) {
		if err := acceptance.CheckStoreUnitBillingStart(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	for _, id := range acceptance.StoreBillingBoundaryScenarios() {
		t.Run(id+"/store-data-sqlite", func(t *testing.T) {
			if err := acceptance.CheckStoreBillingBoundary(t.Context(), t.TempDir(), id); err != nil {
				t.Fatal(err)
			}
		})
	}
	pendingScenarios(t, "billing")
}
