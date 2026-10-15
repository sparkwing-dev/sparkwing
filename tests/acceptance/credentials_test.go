package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestCredentials(t *testing.T) {
	for _, id := range acceptance.StoreCredentialBoundaryScenarios() {
		t.Run(id+"/store-sqlite", func(t *testing.T) {
			if err := acceptance.CheckStoreCredentialBoundary(t.Context(), t.TempDir(), id); err != nil {
				t.Fatal(err)
			}
		})
	}
	pendingScenarios(t, "credentials")
}
