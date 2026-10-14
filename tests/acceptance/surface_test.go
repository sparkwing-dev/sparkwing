package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestSurface(t *testing.T) { pendingScenarios(t, "surface") }

func TestConfigBoundaries(t *testing.T) {
	for _, id := range acceptance.ConfigBoundaryScenarios() {
		t.Run(id, func(t *testing.T) {
			if err := acceptance.CheckConfigBoundary(id); err != nil {
				t.Fatal(err)
			}
		})
	}
}
