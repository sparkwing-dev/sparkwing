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
			switch scenario.ID {
			case "lifecycle-request-and-plan-seals":
				t.Run("hermetic-library", acceptance.CheckOwnerSeals)
			case "recovery-keeper-causes-and-backoff":
				t.Run("hermetic-library", acceptance.CheckKeeperCauses)
			case "recovery-keeper-conflict-and-stop-join":
				t.Run("hermetic-library", acceptance.CheckKeeperConflictAndStop)
			case "recovery-reconciler-waits-and-reclaims":
				t.Run("hermetic-library", acceptance.CheckHostReconciliation)
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
