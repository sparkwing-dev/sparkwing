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
			case "placement-direct-source":
				t.Run("hermetic-native-git", acceptance.CheckDirectSource)
				t.Run("hermetic-bundle-failures", acceptance.CheckSourceBundleFailures)
			case "lifecycle-result-commit-atomicity":
				t.Run("hermetic-output-records", acceptance.CheckOutputRecordCommit)
				t.Run("hermetic-executor-output", acceptance.CheckExecutorOutputCommit)
			case "storage-cas-integrity":
				t.Run("hermetic-required-output", acceptance.CheckOutputRecordRead)
				t.Run("hermetic-required-artifacts", acceptance.CheckArtifactStaging)
				t.Run("hermetic-executor-artifacts", acceptance.CheckExecutorArtifactStaging)
			case "lifecycle-generator-reconstruct-code":
				t.Run("hermetic-replay", acceptance.CheckGeneratorReplay)
			case "environment-startup-versus-body":
				t.Run("hermetic-native-environment-policy", acceptance.CheckSDKEnvironmentProjection)
			case "environment-captured-local-base":
				t.Run("hermetic-private-snapshots", acceptance.CheckCapturedHostEnvironment)
			case "protocol-sdk-secrets-mask-annotations":
				t.Run("hermetic-process-services", acceptance.CheckHostServices)
			case "recovery-application-retry-ordinal":
				t.Run("hermetic-executor-ordinals", acceptance.CheckExecutorEnvelope)
			case "protocol-host-layers-risk-cleanup":
				t.Run("hermetic-claimed-executor", acceptance.CheckExecutorEnvelope)
			case "credentials-secret-identifiers":
				t.Run("hermetic-process-services", acceptance.CheckHostServices)
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
