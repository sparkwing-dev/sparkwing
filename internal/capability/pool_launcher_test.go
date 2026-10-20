package capability_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestVerifiedPoolCannotUseTheInPodRunnerRoute(t *testing.T) {
	p := workPrincipal()
	p.Capability.Subject = capability.Subject{Kind: capability.SubjectMachine, ID: "deploy"}
	p.Capability.Binding = capability.Binding{Kind: capability.BindingNone}
	p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose = "", "", ""
	p.Capability.Trust = capability.TrustVerifiedDefault
	p.Capability.ReservedPools = []string{"deploy"}
	p.Capability.Grants = []capability.Grant{capability.ClaimsLaunch}
	for _, path := range []string{"/api/v1/launcher/claim", "/api/v1/runners/claim"} {
		route, _ := capability.LookupRoute("POST", path)
		auth := capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path}
		for i := range route.Access {
			auth.Live = append(auth.Live, capability.LiveAccess{RuleIndex: i})
		}
		_, err := capability.Authorize(t.Context(), bindingCheck(allowBinding), route, auth, capability.Target{Team: p.Capability.Team, ClaimKinds: []capability.UnitKind{capability.UnitNode}})
		if (err == nil) != (path == "/api/v1/launcher/claim") {
			t.Fatalf("pool route %s: %v", path, err)
		}
	}
}
