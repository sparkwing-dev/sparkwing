package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestStorage(t *testing.T) {
	for _, id := range acceptance.StoreDataBoundaryScenarios() {
		t.Run(id+"/store-data-sqlite", func(t *testing.T) {
			if err := acceptance.CheckStoreDataBoundary(t.Context(), t.TempDir(), id); err != nil {
				t.Fatal(err)
			}
		})
	}
	pendingScenarios(t, "storage")
}
