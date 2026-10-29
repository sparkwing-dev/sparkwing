package capability

import (
	"slices"
	"strings"
	"testing"
)

func TestRoutePolicyDoesNotRetainRemovedSurface(t *testing.T) {
	for _, route := range Routes() {
		key := route.Method + " " + route.Path
		for _, removed := range []string{"/triggers", "/api/v1/nodes", "/api/v1/runs/{run}/nodes", "/api/v1/runs/{run}/children/{child}/nodes", "/gitcache", "/cache-grant", "/bounce", "/dispatches", "/auto-retry", "/usage-metrics", "/operator/cache", "/claim/input"} {
			if strings.Contains(route.Path, removed) && !strings.Contains(route.Path, "/github-app/triggers") {
				t.Errorf("removed surface in target table: %s", key)
			}
		}
	}
}

func TestPrivateNodeHostRoutesRequireBoundClaimAuthority(t *testing.T) {
	for _, operation := range []struct {
		method string
		suffix string
	}{
		{"GET", ""},
		{"GET", "/snapshot"},
		{"GET", "/data-authority"},
		{"POST", "/authentication"},
		{"POST", "/binding"},
		{"POST", "/transition"},
	} {
		path := "/api/v1/runs/{run}/local-host/nodes/{job}" + operation.suffix
		route, ok := LookupRoute(operation.method, path)
		if !ok || route.Authentication != AuthCapability || route.Owner != OwnerRun || len(route.Access) == 0 {
			t.Fatalf("missing private claim policy: %s %s", operation.method, path)
		}
		for _, access := range route.Access {
			read := operation.method == "GET" || operation.suffix == "/authentication" || operation.suffix == "/binding"
			if access.Credential == credentialEval {
				if !(read || operation.suffix == "/transition") || access.Binding != BindRun || access.Liveness != LiveExpansion || !slices.Equal(access.Grants, []Grant{ExpansionSubmit}) {
					t.Errorf("private eval route exceeds its live expansion boundary: %s %+v", path, access)
				}
				continue
			}
			plan := read && access.Binding == BindPlanOwner && slices.Equal(access.Grants, []Grant{DataRead})
			if access.Credential != CredentialClaim || len(access.Grants) == 0 ||
				access.Binding != BindOwnNode && access.Binding != BindRunOfTrigger && !plan ||
				access.Liveness != LiveSensitive && access.Liveness != LiveReporting && access.Liveness != LiveResult {
				t.Errorf("private host route lacks a live claim fence: %s %+v", path, access)
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
	} {
		route, ok := LookupRoute("POST", path)
		if !ok {
			t.Fatalf("missing console route %s", path)
		}
		for _, access := range route.Access {
			if access.Credential != CredentialOperator {
				t.Errorf("%s accepts non-console authority", path)
			}
		}
	}
	for _, route := range Routes() {
		if strings.Contains(route.Path, "/jobs/") && route.Method == "POST" {
			for _, access := range route.Access {
				if slices.Contains(access.Grants, DataWrite) && access.Credential != CredentialPublisher {
					t.Errorf("%s permits a general data writer to report", route.Path)
				}
			}
		}
	}
	input, ok := LookupRoute("GET", "/api/v1/runs/{run}/local-host/nodes/{job}/data-authority")
	if !ok || len(input.Access) != 1 || input.Access[0].Binding != BindOwnNode {
		t.Fatal("job data authority does not enforce its path job")
	}
	plan, ok := LookupRoute("POST", "/api/v1/runs/{run}/plan")
	if !ok {
		t.Fatal("plan route missing")
	}
	for _, access := range plan.Access {
		if access.Credential != CredentialPublisher && access.Binding != BindPlanOwner {
			t.Fatal("non-owner claim can submit a plan")
		}
	}
}

func TestAwardVocabulary(t *testing.T) {
	seen := map[AwardPurpose]bool{}
	for _, policy := range AwardPolicies() {
		if seen[policy.Purpose] {
			t.Errorf("duplicate purpose %s", policy.Purpose)
		}
		seen[policy.Purpose] = true
		grants := map[Grant]bool{}
		for _, grant := range append(slices.Clone(policy.Grants), policy.ClaimGrants...) {
			if !slices.Contains(Grants(), grant) {
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
		if policy.Purpose == PurposeEval && (!slices.Equal(policy.Grants, []Grant{ExpansionSubmit, DataRead, SecretsRelease, RefsRead}) || policy.Unit != UnitEval) {
			t.Fatal("eval grant vocabulary differs from evaluation reads and submission")
		}
		if policy.Purpose == PurposeWork && policy.Unit != UnitNode {
			t.Fatal("work is not persisted as node")
		}
		if policy.Purpose == PurposeHere && (policy.Unit != UnitTrigger || policy.Parent != parentRequester) {
			t.Fatal("here is not a requester-parented trigger award")
		}
	}
	for _, purpose := range []AwardPurpose{PurposeTrigger, PurposePlan, PurposeWork, PurposeEval, PurposeHere} {
		if !seen[purpose] {
			t.Errorf("missing purpose %s", purpose)
		}
	}
	if !slices.Equal(BootstrapGrants(), []Grant{SourceRead, ClaimExchange}) {
		t.Fatal("bootstrap permits more than source and exchange")
	}
	if !slices.Equal(BootstrapUnits(), []UnitKind{UnitPlan, UnitEval, UnitNode}) {
		t.Fatal("bootstrap loses its operational unit")
	}
}

func TestReadOnlyDataOperations(t *testing.T) {
	for _, operation := range []DataOperation{DataDownload, DataHasMany, DataLookup} {
		found := false
		for _, policy := range dataPolicies {
			if policy.Operation != operation {
				continue
			}
			found = true
			if policy.Grant != DataRead || !policy.RequireReadSet || policy.RequireUploader {
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
		route, ok := LookupRoute("POST", "/api/v1/claims/{claim}/"+operation)
		if !ok {
			t.Fatalf("missing eval operation %s", operation)
		}
		found := false
		liveness := liveLogTail
		if operation == "heartbeat" {
			liveness = LiveExpansion
		}
		for _, access := range route.Access {
			if !slices.Contains(access.Grants, ExpansionSubmit) {
				continue
			}
			found = true
			if access.Credential != credentialEval || access.Binding != BindOwnEval || access.Liveness != liveness || len(access.Grants) != 1 {
				t.Errorf("%s allows expansion grant outside own live eval: %+v", operation, access)
			}
		}
		if !found {
			t.Errorf("eval cannot use %s", operation)
		}
	}
}

func TestPaymentCallbackRequiresProviderProof(t *testing.T) {
	route, ok := LookupRoute("POST", "/webhooks/stripe")
	if !ok || route.Authentication != AuthPaymentWebhook || route.Owner != OwnerTeam || route.Retry != RetryExactBody || len(route.Access) != 0 {
		t.Fatal("payment callback can bypass provider proof or checkout-derived tenancy")
	}
	seen := map[string]bool{}
	for _, policy := range paymentEventPolicies {
		if seen[policy.ProviderType] {
			t.Errorf("duplicate provider event %s", policy.ProviderType)
		}
		seen[policy.ProviderType] = true
		if policy.Kind == PaymentCheckoutPaid && !policy.RequirePaid {
			t.Fatal("unpaid session can grant credit")
		}
	}
	if len(seen) != 15 || !seen["payment_intent.requires_action"] {
		t.Fatal("payment event inventory incomplete")
	}
}

func TestPrivateCapacityRoutesRequireLiveExecutionAward(t *testing.T) {
	for _, operation := range []string{"may-start", "relinquish-start"} {
		path := "/api/v1/runs/{run}/local-host/" + operation
		route, ok := LookupRoute("POST", path)
		if !ok || route.Authentication != AuthCapability || route.Owner != OwnerRun || route.Retry != RetryExactBody || len(route.Access) != 1 {
			t.Fatalf("missing start policy: %s %+v", path, route)
		}
		award := route.Access[0]
		if award.Credential != CredentialClaim || award.Binding != BindRun || award.Liveness != LiveSensitive || !slices.Equal(award.Grants, []Grant{NodeReport}) {
			t.Fatalf("start exceeds job award: %s %+v", path, award)
		}
	}
}

func TestTeamAndRunOwnersRefuseATeamlessLauncherRule(t *testing.T) {
	p := Principal{Capability: Capability{Team: ""}}
	for _, owner := range []Owner{OwnerTeam, OwnerRun} {
		if err := checkOwner(p, owner, launcher(), Target{Team: "victim"}); err == nil {
			t.Fatalf("a launcher rule on a %s route skipped the team check", owner)
		}
	}
}
