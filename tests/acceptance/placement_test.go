package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestPlacement(t *testing.T) {
	t.Run("placement-granted-identity-and-capacity/store-sqlite", func(t *testing.T) {
		if err := acceptance.CheckStoreClaimMatching(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	pendingScenarios(t, "placement")
}
