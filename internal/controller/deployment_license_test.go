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
)

// A license that expires while the controller runs must not switch off
// metering, the free tier or sign-in: the declared kind holds until the
// next start, and health carries the alarm.
func TestLicenseExpiryWhileRunningKeepsTheModeAndDegradesHealth(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// The license was valid when the controller started 25 hours ago and
	// expired an hour ago.
	cfg := deploymenttest.Parse(t, deployment.Inputs{Kind: "team", Now: time.Now().Add(-25 * time.Hour)}, license.FeatureMultiTeam)
	h := controller.NewDeployment(st, cfg, nil).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
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

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	var health struct {
		Status   string   `json:"status"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || health.Status != "degraded" || !strings.Contains(strings.Join(health.Problems, ";"), "license: expired") {
		t.Fatalf("health after expiry = %d %s, want 200 degraded naming the license", rec.Code, rec.Body.String())
	}
}
