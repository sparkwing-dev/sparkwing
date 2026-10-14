package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestSettingsBoundaries(t *testing.T) {
	for _, id := range acceptance.SettingsBoundaryScenarios() {
		t.Run(id, func(t *testing.T) {
			if err := acceptance.CheckSettingsBoundary(id); err != nil {
				t.Fatal(err)
			}
		})
	}
}
