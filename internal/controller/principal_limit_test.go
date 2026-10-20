package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
		endpoint := s.budgetEndpoint(Endpoint{Pattern: "/api/v1/claims/{claim}/heartbeat", Authentication: capability.AuthCapability, Handle: func(w http.ResponseWriter, _ *http.Request, _ capability.Authorized) error {
			calls++
			w.WriteHeader(204)
			return nil
		}})
		for i := range 2 {
			r := httptest.NewRequest("POST", "/", nil)
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
