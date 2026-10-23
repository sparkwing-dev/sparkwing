package capability

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestClaimSelectionRequiresEveryKind(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		grants     []Grant
		kinds      []UnitKind
		allowed    bool
	}{
		{"node", "/api/v1/runners/claim", []Grant{NodesClaim}, []UnitKind{UnitNode}, true},
		{"trigger", "/api/v1/runners/claim", []Grant{TriggersClaim}, []UnitKind{UnitTrigger}, true},
		{"both", "/api/v1/runners/claim", []Grant{NodesClaim, TriggersClaim}, []UnitKind{UnitTrigger, UnitNode}, true},
		{"node cannot borrow trigger", "/api/v1/runners/claim", []Grant{NodesClaim}, []UnitKind{UnitNode, UnitTrigger}, false},
		{"trigger cannot borrow node", "/api/v1/runners/claim", []Grant{TriggersClaim}, []UnitKind{UnitNode, UnitTrigger}, false},
		{"runner plans with triggers.claim", "/api/v1/runners/claim", []Grant{TriggersClaim}, []UnitKind{UnitPlan}, true},
		{"runner evaluates with nodes.claim", "/api/v1/runners/claim", []Grant{NodesClaim}, []UnitKind{UnitEval}, true},
		{"nodes.claim cannot plan", "/api/v1/runners/claim", []Grant{NodesClaim, ClaimsLaunch}, []UnitKind{UnitPlan}, false},
		{"triggers.claim cannot evaluate", "/api/v1/runners/claim", []Grant{TriggersClaim, ClaimsLaunch}, []UnitKind{UnitEval}, false},
		{"named run cannot plan", "/api/v1/runs/{run}/claim", []Grant{TriggersClaim, NodesClaim}, []UnitKind{UnitPlan}, false},
		{"launcher", "/api/v1/launcher/claim", []Grant{ClaimsLaunch}, []UnitKind{UnitPlan, UnitEval, UnitNode}, true},
		{"launcher sync has no selection", "/api/v1/launcher/sync", []Grant{ClaimsLaunch}, nil, true},
		{"launcher cannot trigger", "/api/v1/launcher/claim", []Grant{ClaimsLaunch, TriggersClaim}, []UnitKind{UnitTrigger}, false},
		{"empty", "/api/v1/runners/claim", []Grant{NodesClaim}, nil, false},
		{"duplicate", "/api/v1/runners/claim", []Grant{NodesClaim}, []UnitKind{UnitNode, UnitNode}, false},
		{"unknown", "/api/v1/runners/claim", []Grant{NodesClaim}, []UnitKind{"unknown"}, false},
		{"named trigger", "/api/v1/runs/{run}/claim", []Grant{TriggersClaim}, []UnitKind{UnitTrigger}, true},
		{"named trigger cannot select node", "/api/v1/runs/{run}/claim", []Grant{TriggersClaim, NodesClaim}, []UnitKind{UnitNode}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route, ok := LookupRoute("POST", tc.path)
			if !ok {
				t.Fatal("missing claim policy")
			}
			p := workPrincipal()
			p.Capability.Binding = Binding{Kind: BindingNone}
			p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose = "", "", ""
			p.Capability.Grants = tc.grants
			if tc.path == "/api/v1/launcher/claim" || tc.path == "/api/v1/launcher/sync" {
				p.Capability.Subject.Kind = SubjectService
			}
			auth := Authenticated{Principal: p, Method: route.Method, Pattern: route.Path}
			for i := range route.Access {
				auth.Live = append(auth.Live, LiveAccess{RuleIndex: i})
			}
			target := Target{Team: "team", ClaimKinds: tc.kinds}
			if tc.path == "/api/v1/runs/{run}/claim" {
				target.RunID = "run"
			}
			_, err := Authorize(t.Context(), bindingCheck(allowBinding), route, auth, target)
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireCode(t, err, apierr.Forbidden)
		})
	}
}
