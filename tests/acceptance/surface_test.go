package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestSurface(t *testing.T) {
	t.Run("surface-schedules-and-managed-timers/store-sqlite", func(t *testing.T) {
		if err := acceptance.CheckStoreScheduleBoundary(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	pendingScenarios(t, "surface")
}
