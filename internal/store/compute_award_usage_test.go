package store

import "testing"

func TestComputeUsageFollowsLiveBillingAwards(t *testing.T) {
	s, demand := unitClaimFixture(t)
	fundUnitClaim(t, s)
	root := authorityRoot("runner", "a")
	root.RootMetered = true
	rewriteSchemaExec(t, s.db, `DELETE FROM capabilities WHERE prefix='runner'`)
	authorityPersist(t, s, root)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := s.AsOperator().ComputeUsage(t.Context())
	if err != nil || usage.Runners != 1 || usage.ByPrincipal["runner"] != 1 {
		t.Fatalf("live compute: %+v, %v", usage, err)
	}
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	usage, err = s.AsOperator().ComputeUsage(t.Context())
	if err != nil || usage.Runners != 0 {
		t.Fatalf("released compute: %+v, %v", usage, err)
	}
}
