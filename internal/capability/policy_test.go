package capability_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRoutePolicyDoesNotRetainRemovedSurface(t *testing.T) {
	for _, route := range capability.Routes {
		key := route.Method + " " + route.Path
		for _, removed := range []string{"/triggers", "/nodes/", "/gitcache", "/cache-grant", "/bounce", "/dispatches", "/auto-retry", "/usage-metrics", "/operator/cache"} {
			if strings.Contains(route.Path, removed) && !strings.Contains(route.Path, "/github-app/triggers") {
				t.Errorf("removed surface in target table: %s", key)
			}
		}
	}
}

func TestPrivilegedRouteBoundaries(t *testing.T) {
	for _, path := range []string{
		"/api/v1/operator/teams/{team}/trust",
		"/api/v1/operator/teams/{team}/grants",
		"/api/v1/operator/teams/{team}/freeze",
		"/api/v1/operator/teams/{team}/unfreeze",
		"/api/v1/operator/teams/{team}/refunds",
	} {
		route, ok := capability.LookupRoute("POST", path)
		if !ok {
			t.Fatalf("missing console route %s", path)
		}
		for _, access := range route.Access {
			if access.Credential != capability.CredentialOperator {
				t.Errorf("%s accepts non-console authority", path)
			}
		}
	}
	for _, route := range capability.Routes {
		if strings.Contains(route.Path, "/jobs/") && route.Method == "POST" {
			for _, access := range route.Access {
				if slices.Contains(access.Grants, capability.DataWrite) && access.Credential != capability.CredentialPublisher {
					t.Errorf("%s permits a general data writer to report", route.Path)
				}
			}
		}
	}
	input, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/claim/input")
	if !ok || len(input.Access) != 1 || input.Access[0].Binding != capability.BindOwnNode {
		t.Fatal("claim input does not enforce its path job")
	}
	plan, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/plan")
	if !ok {
		t.Fatal("plan route missing")
	}
	for _, access := range plan.Access {
		if access.Credential != capability.CredentialPublisher && access.Binding != capability.BindPlanOwner {
			t.Fatal("non-owner claim can submit a plan")
		}
	}
}

func TestAwardVocabulary(t *testing.T) {
	seen := map[capability.AwardPurpose]bool{}
	for _, policy := range capability.AwardPolicies {
		if seen[policy.Purpose] {
			t.Errorf("duplicate purpose %s", policy.Purpose)
		}
		seen[policy.Purpose] = true
		grants := map[capability.Grant]bool{}
		for _, grant := range append(slices.Clone(policy.Grants), policy.ClaimGrants...) {
			if !slices.Contains(capability.Grants, grant) {
				t.Errorf("unregistered grant %s", grant)
			}
		}
		for _, grant := range policy.Grants {
			if grants[grant] {
				t.Errorf("duplicate grant %s in %s", grant, policy.Unit)
			}
			grants[grant] = true
		}
		if len(policy.Bindings) == 0 || len(policy.ClaimGrants) == 0 || policy.Parent == "" {
			t.Errorf("incomplete policy for %s", policy.Unit)
		}
		if policy.Purpose == capability.PurposeEval && (!slices.Equal(policy.Grants, []capability.Grant{capability.ExpansionSubmit, capability.DataRead}) || policy.Unit != capability.UnitEval) {
			t.Fatal("eval has authority outside expansion and source reads")
		}
		if policy.Purpose == capability.PurposeWork && policy.Unit != capability.UnitNode {
			t.Fatal("work is not persisted as node")
		}
		if policy.Purpose == capability.PurposeHere && (policy.Unit != capability.UnitTrigger || policy.Parent != capability.ParentRequester) {
			t.Fatal("here is not a requester-parented trigger award")
		}
	}
	for _, purpose := range []capability.AwardPurpose{capability.PurposeTrigger, capability.PurposePlan, capability.PurposeWork, capability.PurposeEval, capability.PurposeHere} {
		if !seen[purpose] {
			t.Errorf("missing purpose %s", purpose)
		}
	}
	if !slices.Equal(capability.BootstrapGrants, []capability.Grant{capability.SourceRead, capability.ClaimExchange}) {
		t.Fatal("bootstrap permits more than source and exchange")
	}
	if !slices.Equal(capability.BootstrapUnits, []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}) {
		t.Fatal("bootstrap loses its operational unit")
	}
}

func TestReadOnlyDataOperations(t *testing.T) {
	for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataHasMany, capability.DataLookup} {
		found := false
		for _, policy := range capability.DataPolicies {
			if policy.Operation != operation {
				continue
			}
			found = true
			if policy.Grant != capability.DataRead || !policy.RequireReadSet || policy.RequireUploader {
				t.Errorf("read-only operation has write authority: %+v", policy)
			}
		}
		if !found {
			t.Errorf("read operation %s is missing", operation)
		}
	}
}

func TestEvalLogsRequireOwnUnit(t *testing.T) {
	for _, operation := range []string{"logs", "log-segments", "log-manifest", "heartbeat"} {
		route, ok := capability.LookupRoute("POST", "/api/v1/claims/{claim}/"+operation)
		if !ok {
			t.Fatalf("missing eval operation %s", operation)
		}
		found := false
		for _, access := range route.Access {
			if !slices.Contains(access.Grants, capability.ExpansionSubmit) {
				continue
			}
			found = true
			if access.Credential != capability.CredentialEval || access.Binding != capability.BindOwnEval || access.Liveness != capability.LiveExpansion || len(access.Grants) != 1 {
				t.Errorf("%s allows expansion grant outside own live eval: %+v", operation, access)
			}
		}
		if !found {
			t.Errorf("eval cannot use %s", operation)
		}
	}
}

func TestPaymentCallbackRequiresProviderProof(t *testing.T) {
	route, ok := capability.LookupRoute("POST", "/webhooks/stripe")
	if !ok || route.Authentication != capability.AuthPaymentWebhook || route.Owner != capability.OwnerTeam || route.Retry != capability.RetryExactBody || len(route.Access) != 0 {
		t.Fatal("payment callback can bypass provider proof or checkout-derived tenancy")
	}
	seen := map[string]bool{}
	for _, policy := range capability.PaymentEventPolicies {
		if seen[policy.ProviderType] {
			t.Errorf("duplicate provider event %s", policy.ProviderType)
		}
		seen[policy.ProviderType] = true
		if policy.Kind == capability.PaymentCheckoutPaid && !policy.RequirePaid {
			t.Fatal("unpaid session can grant credit")
		}
	}
	if len(seen) != 14 {
		t.Fatal("payment event inventory incomplete")
	}
}
