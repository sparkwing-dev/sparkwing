package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestRecovery(t *testing.T) {
	for _, id := range acceptance.StoreRecoveryBoundaryScenarios() {
		t.Run(id+"/store-sqlite", func(t *testing.T) {
			if err := acceptance.CheckStoreRecoveryBoundary(t.Context(), t.TempDir(), id); err != nil {
				t.Fatal(err)
			}
		})
	}
	pendingScenarios(t, "recovery")
}
