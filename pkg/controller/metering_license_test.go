package controller_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/license"
)

func TestLegacyMultiTeamLicenseAdvertisesBilling(t *testing.T) {
	f := newCreditsFixtureWithLicense(t, true, license.FeatureMultiTeam)
	status, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/capabilities", f.admin, nil)
	if status != http.StatusOK {
		t.Fatalf("capabilities = %d", status)
	}
	var caps struct {
		Billing struct {
			Enabled bool `json:"enabled"`
		} `json:"billing"`
	}
	if err := json.Unmarshal(body, &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.Billing.Enabled {
		t.Fatal("legacy multi-team license did not advertise billing")
	}
	status, _ = creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits", f.admin, nil)
	if status != http.StatusOK {
		t.Fatalf("licensed credits route = %d, want 200", status)
	}
}
