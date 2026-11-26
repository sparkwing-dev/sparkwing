package controller

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

const (
	routeClaimHeartbeat = "/api/v1/claims/{claim}/heartbeat"
	routeJobTransition  = "/api/v1/runs/{run}/jobs/{job}/transition"
	routeHostTransition = "/api/v1/runs/{run}/local-host/nodes/{job}/transition"
	routeStepFinish     = "/api/v1/runs/{run}/jobs/{job}/steps/finish"
	routeRunSnapshot    = "/api/v1/runs/{run}/local-host/snapshot"
	routeRunnerClaim    = "/api/v1/runners/claim"
)

func routeRule(t *testing.T, method, pattern string, credential capability.CredentialRule) capability.AccessRule {
	t.Helper()
	route, ok := capability.LookupRoute(method, pattern)
	if !ok {
		t.Fatalf("no route %s %s", method, pattern)
	}
	for _, rule := range route.Access {
		if rule.Credential == credential {
			return rule
		}
	}
	t.Fatalf("%s %s has no %s rule", method, pattern, credential)
	return capability.AccessRule{}
}

func teamCaller(team, credential string, rule capability.AccessRule) capability.Authorized {
	return capability.Authorized{
		Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: credential}, Capability: capability.Capability{Team: team}},
		Target:    capability.Target{Team: team},
		Access:    rule,
	}
}

func budgetedCall(s *Server, method, pattern string, a capability.Authorized) *httptest.ResponseRecorder {
	endpoint := s.budgetEndpoint(controllerwire.Endpoint{Method: method, Pattern: pattern, Authentication: capability.AuthCapability, Handle: func(w http.ResponseWriter, _ *http.Request, _ capability.Authorized) error {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}})
	w := httptest.NewRecorder()
	_ = endpoint.Handle(w, httptest.NewRequest(method, "/", nil), a)
	return w
}

func drainTeamBucket(t *testing.T, s *Server, a capability.Authorized) {
	t.Helper()
	read := a
	read.Access = routeRule(t, http.MethodGet, routeRunSnapshot, capability.CredentialClaim)
	for range s.tokenBudget.values().PerTokenMinute + 1 {
		if budgetedCall(s, http.MethodGet, routeRunSnapshot, read).Code == http.StatusTooManyRequests {
			return
		}
	}
	t.Fatal("team bucket never emptied")
}

func TestEmptyTeamBucketStillRenewsLiveLeases(t *testing.T) {
	e, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	e.Server.WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 16})
	poll := `{"runner_version":"v1","kinds":["node"]}`
	w := engineClaimHTTP(t, h, raw, http.MethodPost, routeRunnerClaim, poll, http.StatusOK)
	var response controllerwire.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Claim == nil {
		t.Fatalf("no award: %s: %v", w.Body, err)
	}
	claim := response.Claim
	engineAdmitBearer(t, e.Store, claim.Capability)
	drained := false
	for range 32 {
		r := httptest.NewRequest(http.MethodPost, routeRunnerClaim, strings.NewReader(poll))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if drained = w.Code == http.StatusTooManyRequests; drained {
			break
		}
	}
	if !drained {
		t.Fatal("the team's claim polls never emptied its bucket")
	}
	engineClaimHTTP(t, h, claim.Capability, http.MethodPost, "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, http.StatusOK)
	engineClaimHTTP(t, h, claim.Capability, http.MethodPost, "/api/v1/claims/"+url.PathEscape(claim.UnitID)+"/heartbeat", `{"executing":true}`, http.StatusOK)
	engineClaimHTTP(t, h, raw, http.MethodPost, routeRunnerClaim, poll, http.StatusTooManyRequests)
}

func TestInflightReserveIsBoundedPerTeam(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(nil, slog.New(slog.DiscardHandler)).WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 16})
		claim := routeRule(t, http.MethodPost, routeClaimHeartbeat, capability.CredentialClaim)
		eval := routeRule(t, http.MethodPost, routeHostTransition, capability.CredentialRule("eval_unit"))
		node := teamCaller("a", "node", claim)
		other := teamCaller("b", "other", routeRule(t, http.MethodGet, routeRunSnapshot, capability.CredentialClaim))
		drainTeamBucket(t, s, node)
		if w := budgetedCall(s, http.MethodPost, routeClaimHeartbeat, node); w.Code != http.StatusNoContent {
			t.Fatalf("first renewal past the budget = %d %s", w.Code, w.Body)
		}
		if w := budgetedCall(s, http.MethodPost, routeHostTransition, teamCaller("a", "run-host", eval)); w.Code != http.StatusNoContent {
			t.Fatalf("eval transition past the budget = %d %s", w.Code, w.Body)
		}
		w := budgetedCall(s, http.MethodPost, routeClaimHeartbeat, node)
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
			t.Fatalf("renewal past a spent reserve of %d = %d %q", inflightReserve(16), w.Code, w.Header().Get("Retry-After"))
		}
		if w := budgetedCall(s, http.MethodGet, routeRunSnapshot, other); w.Code != http.StatusNoContent {
			t.Fatalf("another team's read was refused: %d %s", w.Code, w.Body)
		}
	})
}

func TestInflightReserveRefusesUnboundCredentials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(nil, slog.New(slog.DiscardHandler)).WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 16})
		node := teamCaller("a", "node", routeRule(t, http.MethodPost, routeClaimHeartbeat, capability.CredentialClaim))
		drainTeamBucket(t, s, node)
		replay := teamCaller("a", "node", routeRule(t, http.MethodPost, routeJobTransition, capability.CredentialClaim))
		replay.ResultReplay = true
		for name, tc := range map[string]struct {
			method, pattern string
			a               capability.Authorized
		}{
			"publisher transition":               {http.MethodPost, routeJobTransition, teamCaller("a", "publisher", routeRule(t, http.MethodPost, routeJobTransition, capability.CredentialPublisher))},
			"publisher step":                     {http.MethodPost, routeStepFinish, teamCaller("a", "publisher", routeRule(t, http.MethodPost, routeStepFinish, capability.CredentialPublisher))},
			"run-host snapshot":                  {http.MethodGet, routeRunSnapshot, teamCaller("a", "node", routeRule(t, http.MethodGet, routeRunSnapshot, capability.CredentialClaim))},
			"claim poll":                         {http.MethodPost, routeRunnerClaim, teamCaller("a", "runner", routeRule(t, http.MethodPost, routeRunnerClaim, capability.CredentialAny))},
			"result replay":                      {http.MethodPost, routeJobTransition, replay},
			"publisher on a lease-liveness rule": {http.MethodPost, routeJobTransition, teamCaller("a", "publisher", capability.AccessRule{Credential: capability.CredentialPublisher, Grants: []capability.Grant{capability.DataWrite}, Binding: capability.BindData, Liveness: capability.LiveResult})},
		} {
			if w := budgetedCall(s, tc.method, tc.pattern, tc.a); w.Code != http.StatusTooManyRequests {
				t.Errorf("%s past the budget = %d, want 429", name, w.Code)
			}
		}
		if w := budgetedCall(s, http.MethodPost, routeClaimHeartbeat, node); w.Code != http.StatusNoContent {
			t.Fatalf("refused requests spent the reserve: renewal = %d %s", w.Code, w.Body)
		}
	})
}

func TestInflightReserveRefusalsDoNotDelayRenewals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(nil, slog.New(slog.DiscardHandler)).WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 16})
		node := teamCaller("a", "node", routeRule(t, http.MethodPost, routeClaimHeartbeat, capability.CredentialClaim))
		steps := teamCaller("a", "node", routeRule(t, http.MethodPost, routeStepFinish, capability.CredentialClaim))
		drainTeamBucket(t, s, node)
		for range 20 {
			budgetedCall(s, http.MethodPost, routeStepFinish, steps)
		}
		refill := budgetWindow / time.Duration(inflightReserve(16))
		if w := budgetedCall(s, http.MethodPost, routeStepFinish, steps); w.Header().Get("Retry-After") != "30" {
			t.Fatalf("refused step writes deepened the reserve wait: Retry-After %q, want %v", w.Header().Get("Retry-After"), refill)
		}
		time.Sleep(refill)
		if w := budgetedCall(s, http.MethodPost, routeClaimHeartbeat, node); w.Code != http.StatusNoContent {
			t.Fatalf("renewal after one refill = %d %s", w.Code, w.Body)
		}
	})
}

func TestInflightReserveIsSharedByDefaultTeamCredentials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(nil, slog.New(slog.DiscardHandler)).WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 16})
		rule := routeRule(t, http.MethodPost, routeClaimHeartbeat, capability.CredentialClaim)
		first, second := teamCaller("default", "claim-one", rule), teamCaller("default", "claim-two", rule)
		drainTeamBucket(t, s, first)
		drainTeamBucket(t, s, second)
		for i, tc := range []struct {
			a    capability.Authorized
			want int
		}{{first, http.StatusNoContent}, {second, http.StatusNoContent}, {first, http.StatusTooManyRequests}} {
			if w := budgetedCall(s, http.MethodPost, routeClaimHeartbeat, tc.a); w.Code != tc.want {
				t.Fatalf("renewal %d = %d, want %d: %s", i+1, w.Code, tc.want, w.Body)
			}
		}
	})
}

func TestInflightRetriesKeepTheTeamBucketAvailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(nil, slog.New(slog.DiscardHandler)).WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 16})
		node := teamCaller("a", "node", routeRule(t, http.MethodPost, routeClaimHeartbeat, capability.CredentialClaim))
		read := teamCaller("a", "node", routeRule(t, http.MethodGet, routeRunSnapshot, capability.CredentialClaim))
		drainTeamBucket(t, s, node)
		admitted := 0
		for i := range 600 {
			if budgetedCall(s, http.MethodPost, routeClaimHeartbeat, node).Code == http.StatusNoContent {
				admitted++
			}
			if i%10 == 0 {
				budgetedCall(s, http.MethodGet, routeRunSnapshot, read)
			}
			time.Sleep(time.Second)
		}
		if reserveOnly := 10 * inflightReserve(16); admitted < 100 {
			t.Fatalf("renewals retried each second for 10 minutes: %d of 600 admitted, near the reserve's %d; refused renewals are pinning the team bucket", admitted, reserveOnly)
		}
	})
}
