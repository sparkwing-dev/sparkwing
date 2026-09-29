package logs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A claim token's append is refused when the controller's validation names
// no team to account it to, and the controller is asked on every append, even
// one naming a claim generation, so nothing about a claim is cached.
func TestClaimTokenAppend_IsValidatedEveryTimeAndAlwaysCarriesATeam(t *testing.T) {
	team, validations := "", 0
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim/validate") {
			validations++
			if team != "" {
				w.Header().Set(store.ClaimTeamHeader, team)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "unexpected", http.StatusTeapot)
	}))
	t.Cleanup(ctrl.Close)
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := s.WithControllerAuth(ctrl.URL, MaxClaimCacheTTL).Handler()
	appendAs := func(token string, headers map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/logs/run-1/a", strings.NewReader("{\"msg\":\"x\"}\n"))
		req.Header.Set("Authorization", "Bearer "+token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := appendAs("swc_claim", nil); code != http.StatusBadGateway {
		t.Fatalf("an append the controller named no team for = %d, want 502", code)
	}
	team = "acme"
	attempt := map[string]string{
		store.ClaimHolderHeader: "h", store.ClaimMembershipHeader: "m", store.ClaimReservationHeader: "r",
		store.ClaimGenerationHeader: "3", store.AttemptOrdinalHeader: "1",
	}
	before := validations
	for range 2 {
		if code := appendAs("swc_claim", attempt); code != http.StatusNoContent {
			t.Fatalf("a claim token's append = %d, want 204", code)
		}
	}
	if validations-before != 2 {
		t.Fatalf("two claim-token appends asked the controller %d times, want 2", validations-before)
	}
}
