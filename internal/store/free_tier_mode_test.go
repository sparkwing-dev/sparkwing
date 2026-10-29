package store

import (
	"fmt"
	"testing"
)

// A deployment without metering has no free tier, whatever its teams are called: a licensed self-hosted team
// controller's teams never had credits, so the free download cap, secret count and Actions binding count would refuse
// work nobody can pay to unblock.
func TestUnmeteredDeploymentAppliesNoFreeTierLimits(t *testing.T) {
	for _, tc := range []struct {
		team    Team
		metered bool
		want    bool
	}{{"a", true, true}, {DefaultTeam, true, false}, {"a", false, false}, {DefaultTeam, false, false}} {
		s := &Store{}
		if !tc.metered {
			s.DisableCreditMetering()
		}
		if got := s.holdsFreeAllowance(tc.team); got != tc.want {
			t.Errorf("team %s metered %v: free tier %v, want %v", tc.team, tc.metered, got, tc.want)
		}
	}

	t.Run("downloads", func(t *testing.T) {
		s := authorityStore(t)
		s.DisableCreditMetering()
		tenant := &Tenant{s: s, team: "a"}
		for range 3 {
			got, err := tenant.ChargeDownload(t.Context(), DownloadCharge{Bytes: 4 << 30, FreeCapBytes: 5 << 30, FundedCapBytes: 50 << 30})
			if err != nil || got.Tier != teamTierFunded {
				t.Fatalf("unmetered download charged %+v, %v", got, err)
			}
		}
	})
	t.Run("secrets", func(t *testing.T) {
		tenant, a := credentialMaintenanceFixture(t)
		tenant.s.DisableCreditMetering()
		for i := range maxSecretsPerTeam + 1 {
			if _, err := tenant.WriteSecret(t.Context(), a, sealedSecretFixture(t, tenant, Secret{Name: fmt.Sprintf("KEY_%d", i), Value: "sealed"})); err != nil {
				t.Fatalf("secret %d: %v", i, err)
			}
		}
	})
	t.Run("actions bindings", func(t *testing.T) {
		tenant, a, _ := actionsEnrollment(t)
		tenant.s.DisableCreditMetering()
		for i := range maxGitHubRunnerBindings {
			if _, err := tenant.RegisterGitHubRunner(t.Context(), a, GitHubRunnerBinding{RepositoryID: int64(i + 100), RepositoryOwnerID: 7, Repository: "acme/repo"}); err != nil {
				t.Fatalf("binding %d: %v", i, err)
			}
		}
	})
}
