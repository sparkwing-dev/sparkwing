package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// A trigger claim streaming its run's events loses the stream once the run is
// cancelled: the recheck is the route's own rule, claim liveness included.
func TestReauthorizeEndsAClaimReaderWhenItsRunIsCancelled(t *testing.T) {
	tenant, auth := sourceReleaseUnitFixture(t, capability.UnitTrigger)
	route, ok := capability.LookupRoute("GET", "/api/v1/runs/{run}/events/stream")
	if !ok {
		t.Fatal("events stream route is missing")
	}
	var claimRule capability.AccessRule
	for _, rule := range route.Access {
		if rule.Credential == capability.CredentialClaim {
			claimRule = rule
		}
	}
	a := capability.Authorized{Principal: auth.Principal, Binding: auth.Binding, Target: capability.Target{Team: "a", RunID: "run"}, Access: claimRule}
	if err := tenant.Reauthorize(t.Context(), a, route.Method, route.Path); err != nil {
		t.Fatalf("a live trigger claim was refused: %v", err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=1,cancel_reason='user' WHERE id='run'`)
	if err := tenant.Reauthorize(t.Context(), a, route.Method, route.Path); err == nil {
		t.Fatal("a trigger claim kept reading a cancelled run")
	}
}
