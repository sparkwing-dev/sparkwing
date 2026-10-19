package capability_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestClaimSelectionRequiresEveryKind(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		grants     []capability.Grant
		kinds      []capability.UnitKind
		allowed    bool
	}{
		{"node", "/api/v1/runners/claim", []capability.Grant{capability.NodesClaim}, []capability.UnitKind{capability.UnitNode}, true},
		{"trigger", "/api/v1/runners/claim", []capability.Grant{capability.TriggersClaim}, []capability.UnitKind{capability.UnitTrigger}, true},
		{"both", "/api/v1/runners/claim", []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}, true},
		{"node cannot borrow trigger", "/api/v1/runners/claim", []capability.Grant{capability.NodesClaim}, []capability.UnitKind{capability.UnitNode, capability.UnitTrigger}, false},
		{"trigger cannot borrow node", "/api/v1/runners/claim", []capability.Grant{capability.TriggersClaim}, []capability.UnitKind{capability.UnitNode, capability.UnitTrigger}, false},
		{"runner plans with triggers.claim", "/api/v1/runners/claim", []capability.Grant{capability.TriggersClaim}, []capability.UnitKind{capability.UnitPlan}, true},
		{"runner evaluates with nodes.claim", "/api/v1/runners/claim", []capability.Grant{capability.NodesClaim}, []capability.UnitKind{capability.UnitEval}, true},
		{"nodes.claim cannot plan", "/api/v1/runners/claim", []capability.Grant{capability.NodesClaim, capability.ClaimsLaunch}, []capability.UnitKind{capability.UnitPlan}, false},
		{"triggers.claim cannot evaluate", "/api/v1/runners/claim", []capability.Grant{capability.TriggersClaim, capability.ClaimsLaunch}, []capability.UnitKind{capability.UnitEval}, false},
		{"named run cannot plan", "/api/v1/runs/{run}/claim", []capability.Grant{capability.TriggersClaim, capability.NodesClaim}, []capability.UnitKind{capability.UnitPlan}, false},
		{"launcher", "/api/v1/launcher/claim", []capability.Grant{capability.ClaimsLaunch}, []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}, true},
		{"launcher sync has no selection", "/api/v1/launcher/sync", []capability.Grant{capability.ClaimsLaunch}, nil, true},
		{"launcher cannot trigger", "/api/v1/launcher/claim", []capability.Grant{capability.ClaimsLaunch, capability.TriggersClaim}, []capability.UnitKind{capability.UnitTrigger}, false},
		{"empty", "/api/v1/runners/claim", []capability.Grant{capability.NodesClaim}, nil, false},
		{"duplicate", "/api/v1/runners/claim", []capability.Grant{capability.NodesClaim}, []capability.UnitKind{capability.UnitNode, capability.UnitNode}, false},
		{"unknown", "/api/v1/runners/claim", []capability.Grant{capability.NodesClaim}, []capability.UnitKind{"unknown"}, false},
		{"named trigger", "/api/v1/runs/{run}/claim", []capability.Grant{capability.TriggersClaim}, []capability.UnitKind{capability.UnitTrigger}, true},
		{"named trigger cannot select node", "/api/v1/runs/{run}/claim", []capability.Grant{capability.TriggersClaim, capability.NodesClaim}, []capability.UnitKind{capability.UnitNode}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route, ok := capability.LookupRoute("POST", tc.path)
			if !ok {
				t.Fatal("missing claim policy")
			}
			p := workPrincipal()
			p.Capability.Binding = capability.Binding{Kind: capability.BindingNone}
			p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose = "", "", ""
			p.Capability.Grants = tc.grants
			if tc.path == "/api/v1/launcher/claim" || tc.path == "/api/v1/launcher/sync" {
				p.Capability.Subject.Kind = capability.SubjectService
			}
			auth := capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path}
			for i := range route.Access {
				auth.Live = append(auth.Live, capability.LiveAccess{RuleIndex: i})
			}
			target := capability.Target{Team: "team", ClaimKinds: tc.kinds}
			if tc.path == "/api/v1/runs/{run}/claim" {
				target.RunID = "run"
			}
			_, err := capability.Authorize(t.Context(), bindingCheck(allowBinding), route, auth, target)
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
