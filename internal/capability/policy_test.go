package capability_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRouteRegistry(t *testing.T) {
	mux := http.NewServeMux()
	var registered []string
	for _, route := range capability.Routes {
		key := route.Method + " " + route.Path
		mux.HandleFunc(key, func(http.ResponseWriter, *http.Request) {})
		registered = append(registered, key)
		for _, removed := range []string{"/triggers", "/nodes/", "/gitcache", "/cache-grant", "/bounce", "/dispatches", "/auto-retry", "/usage-metrics", "/operator/cache"} {
			if strings.Contains(route.Path, removed) && !strings.Contains(route.Path, "/github-app/triggers") {
				t.Errorf("removed surface in target table: %s", key)
			}
		}
	}
	if err := capability.ValidateRoutes(registered); err != nil {
		t.Fatal(err)
	}
	t.Run("unclassified registration", func(t *testing.T) {
		if err := capability.ValidateRoutes(append(slices.Clone(registered), "POST /api/v1/unclassified")); err == nil {
			t.Fatal("unclassified handler accepted")
		}
	})
	t.Run("unimplemented policy", func(t *testing.T) {
		if err := capability.ValidateRoutes(registered[1:]); err == nil {
			t.Fatal("policy without a handler accepted")
		}
	})
	t.Run("duplicate handler", func(t *testing.T) {
		if err := capability.ValidateRoutes(append(slices.Clone(registered), registered[0])); err == nil {
			t.Fatal("duplicate handler accepted")
		}
	})
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
	seen := map[capability.UnitKind]bool{}
	for _, policy := range capability.AwardPolicies {
		if seen[policy.Unit] {
			t.Errorf("duplicate unit %s", policy.Unit)
		}
		seen[policy.Unit] = true
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
		if policy.Unit == capability.UnitBootstrap && (len(policy.Grants) != 2 || !grants[capability.SourceRead] || !grants[capability.ClaimExchange]) {
			t.Fatal("bootstrap permits more than source and exchange")
		}
	}
	for _, unit := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitWork, capability.UnitEval, capability.UnitHere, capability.UnitBootstrap} {
		if !seen[unit] {
			t.Errorf("missing unit %s", unit)
		}
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
