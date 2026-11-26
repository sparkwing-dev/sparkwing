package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func budgetRequest(s *Server, runner string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, namedClaimRequest(runner))
	return w
}

func TestRequestBudgetShedsClaimsAndSeparatesRunners(t *testing.T) {
	s := newAdviceServer(t).WithRequestBudget(RequestBudget{ClaimsPerMinute: 1})
	for _, tc := range []struct {
		runner string
		want   int
	}{{"first", 200}, {"first", 429}, {"second", 200}, {"second", 429}} {
		w := budgetRequest(s, tc.runner)
		if w.Code != tc.want || w.Code == 429 && w.Header().Get("Retry-After") == "" {
			t.Fatalf("%s=%d %s", tc.runner, w.Code, w.Body)
		}
		if w.Code == 429 {
			for _, want := range []string{"limit is 1", "usage is at least 1", "ask the operator"} {
				if !strings.Contains(w.Body.String(), want) {
					t.Fatalf("claim refusal omits %q: %s", want, w.Body.String())
				}
			}
		}
	}
}

func TestRequestBudgetDefaultsToUnlimited(t *testing.T) {
	s := newAdviceServer(t)
	for range 24 {
		if w := budgetRequest(s, "runner"); w.Code != 200 {
			t.Fatalf("default=%d %s", w.Code, w.Body)
		}
	}
}

func TestRequestBudgetCapsRunnerNames(t *testing.T) {
	s := newAdviceServer(t).WithRequestBudget(RequestBudget{ClaimsPerMinute: 100, RunnersPerToken: 2})
	for _, tc := range []struct {
		runner string
		want   int
	}{{"first", 200}, {"second", 200}, {"third", 429}, {"first", 200}} {
		w := budgetRequest(s, tc.runner)
		if w.Code != tc.want {
			t.Fatalf("%s=%d %s", tc.runner, w.Code, w.Body)
		}
		if w.Code == 429 && (!strings.Contains(w.Body.String(), "limit is 2") || !strings.Contains(w.Body.String(), "current usage is 2") || !strings.Contains(w.Body.String(), "ask the operator")) {
			t.Fatalf("runner-name refusal lacks usage and remedy: %s", w.Body.String())
		}
	}
}

func TestRequestBudgetHeartbeatUsesAuthenticatedHolderProperty(t *testing.T) {
	property := func(value string) bool {
		s := New(nil, nil).WithRequestBudget(RequestBudget{HeartbeatsPerMinute: 1})
		a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}}, Binding: capability.Binding{Holder: "bound"}}
		calls := 0
		endpoint := s.budgetEndpoint(controllerwire.Endpoint{Pattern: "/api/v1/claims/{claim}/heartbeat", Authentication: capability.AuthCapability, Handle: func(w http.ResponseWriter, _ *http.Request, _ capability.Authorized) error {
			calls++
			w.WriteHeader(http.StatusNoContent)
			return nil
		}})
		for i := range 2 {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Header.Set(store.RunnerIdentityHeader, value)
			r.Header.Set("X-Sparkwing-Claim-Holder", value)
			w := httptest.NewRecorder()
			if endpoint.Handle(w, r, a) != nil {
				return false
			}
			if i == 0 && w.Code != 204 || i == 1 && (w.Code != 429 || w.Header().Get("Retry-After") == "") {
				return false
			}
		}
		return calls == 1
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestRequestBudgetRecommendationsClearShippedCadence(t *testing.T) {
	if CompliantClaimPollsPerMinute() != int(time.Minute/ClaimPollInterval) {
		t.Fatal("recommended claim budget does not follow the claim poll cadence")
	}
	renewalsPerClaimMinute := int(time.Minute / limits.Renew(limits.DefaultLease))
	if claims := RecommendedHeartbeatsPerMinute / (10 * renewalsPerClaimMinute); claims != 120 {
		t.Fatalf("recommended heartbeat budget covers %d concurrent claims at ten times the renewal cadence, documented as 120", claims)
	}
}

func TestRequestBudgetFleetStaysAlive(t *testing.T) {
	s := newAdviceServer(t).WithRequestBudget(RequestBudget{ClaimsPerMinute: CompliantClaimPollsPerMinute(), HeartbeatsPerMinute: RecommendedHeartbeatsPerMinute})
	for _, runner := range []string{"one", "two", "three"} {
		for range CompliantClaimPollsPerMinute() {
			if w := budgetRequest(s, runner); w.Code != 200 {
				t.Fatalf("%s=%d %s", runner, w.Code, w.Body)
			}
		}
	}
}

func TestRequestBudgetNeverLogsRunnerHeader(t *testing.T) {
	for _, prefix := range capability.CredentialPrefixes() {
		for _, class := range []string{budgetClassBeat, budgetClassRunnerNames} {
			s := New(nil, nil).WithRequestBudget(RequestBudget{HeartbeatsPerMinute: 1, RunnersPerToken: 1})
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}}}
			credential := prefix + strings.Repeat("x", 43)
			for i := range 2 {
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				identity := credential
				if class == budgetClassRunnerNames && i == 0 {
					identity = "first"
				}
				r.Header.Set(store.RunnerIdentityHeader, identity)
				s.admitRunnerRequest(httptest.NewRecorder(), r, a, budgetClassBeat)
			}
			if !strings.Contains(logs.String(), "request shed") || strings.Contains(logs.String(), credential) {
				t.Fatalf("unsafe %s warning: %s", class, &logs)
			}
			records := auditLines(t, &logs, "request shed")
			if len(records) != 1 || records[0]["level"] != "WARN" || records[0]["route_class"] != class {
				t.Fatalf("request budget refusal must warn: %s", &logs)
			}
		}
	}
}

func TestRequestBudgetBoundsRunnerHeader(t *testing.T) {
	s := New(nil, nil).WithRequestBudget(RequestBudget{})
	a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}}}
	prefix := strings.Repeat("x", 4096)
	keys := make(map[string]bool)
	for _, identity := range []string{prefix + "one", prefix + "two"} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set(store.RunnerIdentityHeader, identity)
		key, ok := s.runnerBudgetKey(httptest.NewRecorder(), r, a)
		if !ok || len(key) > 256 || keys[key] {
			t.Fatalf("unbounded or colliding runner budget key: length=%d", len(key))
		}
		keys[key] = true
	}
}

func TestTokenRequestBudgetRefusalNamesLimitUsageAndOperator(t *testing.T) {
	s := newAdviceServer(t).WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 1})
	if w := budgetRequest(s, "first"); w.Code != 200 {
		t.Fatalf("first request: %d %s", w.Code, w.Body)
	}
	w := budgetRequest(s, "second")
	if w.Code != 429 {
		t.Fatalf("token budget: %d %s", w.Code, w.Body)
	}
	for _, want := range []string{"limit is 1 per minute", "usage is at least 1", "ask the operator"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("token refusal omits %q: %s", want, w.Body.String())
		}
	}
}

func TestIdentityLinkBudgetRefusalNamesLimitUsageAndSupport(t *testing.T) {
	s := New(nil, nil)
	session := &store.Session{AccountID: "linking-account"}
	for range identityLinkAttemptsPerMinute {
		if err := s.admitIdentityLink(session); err != nil {
			t.Fatal(err)
		}
	}
	err := s.admitIdentityLink(session)
	if err == nil {
		t.Fatal("11th identity link attempt accepted")
	}
	for _, want := range []string{"limit is 10", "current usage is 10", "support@sparkwing.dev"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("link refusal %q omits %q", err, want)
		}
	}
}

func TestIdlePollBudgetSeparatesPersonalAndHereClaimants(t *testing.T) {
	s := New(nil, nil).WithIdleClaimPollEnforced(true)
	s.recordQueueActivity(time.Now().Add(-testIdleWindow))
	for _, c := range []capability.Capability{
		{Team: "team", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "mybox"}},
		{Team: "team", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "desktop"}},
		{Team: "team", Purpose: capability.PurposeHere, Binding: capability.Binding{RunID: "here-run"}},
	} {
		a := capability.Authorized{Principal: capability.Principal{Capability: c}}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runners/claim", nil)
		if _, ok := s.admitRunnerRequest(httptest.NewRecorder(), r, a, budgetClassClaim); !ok {
			t.Fatalf("another claimant spent the first poll for %+v", c.Subject)
		}
		if _, ok := s.admitRunnerRequest(httptest.NewRecorder(), r, a, budgetClassClaim); ok {
			t.Fatal("early repeated poll admitted")
		}
	}
}

func TestTokenRequestBudgetExemptsOnlyTheCloudLauncher(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	engine.claimProfiles = engineProfileFake{}
	engine.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	launcher := engineHTTPRoot(t, engine, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	customer := engineHTTPRoot(t, engine, "a", "customer", []capability.Grant{capability.NodesClaim})
	poolLauncher := teamPoolLauncherRoot(t, engine, "a", "pool-launcher")
	operator := engineHTTPRoot(t, engine, "", "operator", []capability.Grant{capability.OperatorLimits})
	mixed := engineHTTPRoot(t, engine, "", "mixed-launcher", []capability.Grant{capability.ClaimsLaunch, capability.OperatorLimits})
	var logs bytes.Buffer
	s := New(engine.Store, slog.New(slog.NewJSONHandler(&logs, nil))).WithEngineController(engine.EngineController).
		WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 1, AlarmPerMinute: 3})
	h := s.Handler()
	refusals := func(raw, method, path, body string) int {
		refused := 0
		for range 4 {
			r := httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+raw)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(store.RunnerIdentityHeader, "launcher")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code == http.StatusTooManyRequests {
				refused++
			} else if w.Code >= 400 {
				t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
			}
		}
		return refused
	}
	claim := `{"runner_version":"v1","kinds":["node"]}`
	if got := refusals(launcher, http.MethodPost, "/api/v1/launcher/claim", claim); got != 0 {
		t.Fatalf("Cloud launcher claims refused %d times past the budget", got)
	}
	if got := refusals(launcher, http.MethodPost, "/api/v1/launcher/sync", `{}`); got != 0 {
		t.Fatalf("Cloud launcher syncs refused %d times past the budget", got)
	}
	if got := refusals(mixed, http.MethodPost, "/api/v1/launcher/claim", claim) + refusals(mixed, http.MethodPost, "/api/v1/launcher/sync", `{}`); got != 0 {
		t.Fatalf("mixed-grant Cloud launcher refused %d times on launcher routes past the budget", got)
	}
	if !strings.Contains(logs.String(), "controller request rate above its alarm") {
		t.Fatalf("Cloud launcher requests left the aggregate alarm: %s", &logs)
	}
	for name, tc := range map[string]struct{ raw, method, path, body string }{
		"customer naming itself launcher":     {customer, http.MethodPost, "/api/v1/runners/claim", claim},
		"team pool launcher":                  {poolLauncher, http.MethodPost, "/api/v1/launcher/claim", claim},
		"teamless operator service":           {operator, http.MethodGet, "/api/v1/operator/limits", ""},
		"mixed-grant launcher off its routes": {mixed, http.MethodGet, "/api/v1/operator/limits", ""},
	} {
		if got := refusals(tc.raw, tc.method, tc.path, tc.body); got == 0 {
			t.Errorf("%s: never refused past the budget", name)
		}
	}
}

func teamPoolLauncherRoot(t *testing.T, e *engineFixture, team, prefix string) string {
	t.Helper()
	secret := sha256.Sum256([]byte(prefix))
	raw := "swk_" + base64.RawURLEncoding.EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(raw))
	c := capability.Capability{
		Prefix: prefix, Team: team, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: prefix},
		Audience: capability.Controller, Grants: []capability.Grant{capability.ClaimsLaunch}, Binding: capability.Binding{Kind: capability.BindingNone},
		Issuance: capability.Delegate, ReservedPools: []string{"pool"}, IssuedAt: time.Unix(0, 1).UTC(),
	}
	if !capability.Launcher(c) || capability.CloudLauncher(c) {
		t.Fatal("fixture is not a team-pool launcher")
	}
	envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: c})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `INSERT INTO capabilities(digest,prefix,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,repo_scope,created_at,created_by) VALUES (?,?,?,'machine',?,'controller',?,'delegate',1,?,'none',?,?,1,?)`,
		hex.EncodeToString(digest[:]), prefix, team, prefix, []byte(`["claims.launch"]`), envelope, []byte(`["pool"]`), []byte(`null`), prefix)
	return raw
}
