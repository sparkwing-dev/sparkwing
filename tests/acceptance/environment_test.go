package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestEnvironment(t *testing.T) {
	pendingScenarios(t, "environment")
}

func TestEnvironmentBoundaries(t *testing.T) {
	for _, id := range acceptance.EnvironmentBoundaryScenarios() {
		t.Run(id, func(t *testing.T) {
			if err := acceptance.CheckEnvironmentBoundary(id); err != nil {
				t.Fatal(err)
			}
		})
	}
}
