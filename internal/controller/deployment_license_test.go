package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// A license that expires while the controller runs must not switch off
// metering, the free tier or sign-in: the declared kind holds until the
// next start, and the operator learns of it from the log and operator health.
// Public health answers only liveness and readiness, so it does not tell
// anyone the license lapsed.
func TestLicenseExpiryWhileRunningKeepsTheModeAndHidesItFromPublicHealth(t *testing.T) {
	// The license was valid when the controller started 25 hours ago and
	// expired an hour ago.
	cfg := deploymenttest.Parse(t, deployment.Inputs{Kind: "team", Now: time.Now().Add(-25 * time.Hour)}, license.FeatureMultiTeam)
	var srv *controller.Server
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), store.WithPolicies(func(st *store.Store) (store.Policies, error) {
		srv = controller.NewDeployment(st, cfg, nil)
		return srv.StorePolicies(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h := srv.Handler()
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := get("/api/v1/capabilities")
	var caps struct {
		Teams   struct{ Enabled bool } `json:"teams"`
		Billing struct{ Enabled bool } `json:"billing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.Teams.Enabled || !caps.Billing.Enabled {
		t.Fatalf("capabilities after expiry = %s", rec.Body.String())
	}

	if rec := get("/api/v1/health"); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok","auth":"enabled"}` {
		t.Fatalf("public health after expiry = %d %s, want only liveness", rec.Code, rec.Body.String())
	}
}
