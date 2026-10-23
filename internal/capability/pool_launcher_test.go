package capability

import (
	"testing"
)

func TestVerifiedPoolCannotUseTheInPodRunnerRoute(t *testing.T) {
	p := workPrincipal()
	p.Capability.Subject = Subject{Kind: SubjectMachine, ID: "deploy"}
	p.Capability.Binding = Binding{Kind: BindingNone}
	p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose = "", "", ""
	p.Capability.Trust = TrustVerifiedDefault
	p.Capability.ReservedPools = []string{"deploy"}
	p.Capability.Grants = []Grant{ClaimsLaunch}
	for _, path := range []string{"/api/v1/launcher/claim", "/api/v1/runners/claim"} {
		route, _ := LookupRoute("POST", path)
		auth := Authenticated{Principal: p, Method: route.Method, Pattern: route.Path}
		for i := range route.Access {
			auth.Live = append(auth.Live, LiveAccess{RuleIndex: i})
		}
		_, err := Authorize(t.Context(), bindingCheck(allowBinding), route, auth, Target{Team: p.Capability.Team, ClaimKinds: []UnitKind{UnitNode}})
		if (err == nil) != (path == "/api/v1/launcher/claim") {
			t.Fatalf("pool route %s: %v", path, err)
		}
	}
}
