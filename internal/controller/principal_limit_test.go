package controller

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
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
		if w := budgetRequest(s, tc.runner); w.Code != tc.want {
			t.Fatalf("%s=%d %s", tc.runner, w.Code, w.Body)
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
	if CompliantClaimPollsPerMinute() != int(time.Minute/ClaimPollInterval) || RecommendedHeartbeatsPerMinute < 2*int(time.Minute/(3*time.Second)) {
		t.Fatal("recommended budget cannot sustain the runner cadence")
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
