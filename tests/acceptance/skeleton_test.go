package acceptance_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func pendingScenarios(t *testing.T, group string) {
	t.Helper()
	count := 0
	for _, scenario := range acceptance.Scenarios() {
		if scenario.Group != group {
			continue
		}
		count++
		t.Run(scenario.ID, func(t *testing.T) {
			if check, ok := runspecConfigBoundaries()[scenario.ID]; ok {
				t.Run("runspec-config", func(t *testing.T) {
					if err := check(scenario.ID); err != nil {
						t.Fatal(err)
					}
				})
			}
			var areas []string
			for _, area := range scenario.Requires {
				areas = append(areas, string(area))
			}
			for _, target := range acceptance.Targets() {
				if !acceptance.Applies(scenario, target) {
					continue
				}
				t.Run(target.ID, func(t *testing.T) {
					t.Skipf("contract skeleton: requires %s and the T1 driver; %s; no product assertions executed",
						strings.Join(areas, ", "), strings.Join(scenario.Sections, ", "))
				})
			}
		})
	}
	if count == 0 {
		t.Fatalf("no acceptance scenarios registered for group %q", group)
	}
}

func runspecConfigBoundaries() map[string]func(string) error {
	out := map[string]func(string) error{}
	for _, id := range acceptance.EnvironmentBoundaryScenarios() {
		out[id] = acceptance.CheckEnvironmentBoundary
	}
	for _, id := range acceptance.SettingsBoundaryScenarios() {
		out[id] = acceptance.CheckSettingsBoundary
	}
	for _, id := range acceptance.ConfigBoundaryScenarios() {
		out[id] = acceptance.CheckConfigBoundary
	}
	return out
}
