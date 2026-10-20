package controller_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestLegacyMultiTeamLicenseAdvertisesBilling(t *testing.T) {
	f := newCreditsFixture(t)
	status, body := f.request(t, http.MethodGet, "/api/v1/capabilities", f.admin, nil)
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
	status, _ = f.request(t, http.MethodGet, "/api/v1/credits", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("licensed credits route = %d, want 200", status)
	}
}
