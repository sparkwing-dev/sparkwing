package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestLifecycle(t *testing.T) {
	t.Run("lifecycle-transition-replay-and-fencing/store-sqlite", func(t *testing.T) {
		if err := acceptance.CheckStoreTransitions(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("lifecycle-request-and-plan-seals/store-sqlite", func(t *testing.T) {
		if err := acceptance.CheckStorePlanSeals(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	pendingScenarios(t, "lifecycle")
}
