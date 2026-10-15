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
			case "lifecycle-generator-reconstruct-code":
				t.Run("hermetic-replay", acceptance.CheckGeneratorReplay)
			case "environment-startup-versus-body":
				t.Run("hermetic-native-environment-policy", acceptance.CheckSDKEnvironmentProjection)
			case "protocol-host-layers-risk-cleanup":
				t.Run("hermetic-claimed-executor", acceptance.CheckExecutorEnvelope)
			case "credentials-secret-identifiers":
				t.Run("hermetic-planning-defaults", acceptance.CheckPlanningDefaults)
			case "lifecycle-approval-decisions-and-owner-rights":
				t.Run("hermetic-policy-evaluation", acceptance.CheckOwnerApprovalPolicies)
			case "lifecycle-generator-barrier":
				t.Run("hermetic-library", acceptance.CheckOwnerExpansion)
			case "lifecycle-request-and-plan-seals":
				t.Run("hermetic-library", acceptance.CheckOwnerSeals)
			case "recovery-keeper-causes-and-backoff":
				t.Run("hermetic-library", acceptance.CheckKeeperCauses)
				t.Run("hermetic-claimed-node", acceptance.CheckClaimedNodeLifecycle)
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
