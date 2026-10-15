package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestPlacement(t *testing.T) {
	t.Run("placement-run-host-to-launcher/store-sqlite", func(t *testing.T) {
		if err := acceptance.CheckStoreLauncherSync(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("placement-github-actions-exchange/store-sqlite", func(t *testing.T) {
		if err := acceptance.CheckStoreActionsEnrollment(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("placement-ordered-matching/store-sqlite", func(t *testing.T) {
		if err := acceptance.CheckStoreQueueOrder(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("placement-granted-identity-and-capacity/store-sqlite", func(t *testing.T) {
		if err := acceptance.CheckStoreClaimMatching(t.Context(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
	pendingScenarios(t, "placement")
}
