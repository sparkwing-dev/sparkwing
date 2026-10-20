package controller

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestReviewNativeEnrollmentRepositoryClaim(t *testing.T) {
	for _, display := range []string{"https://github.com/acme/repo", "https://github.com/acme-evil/repo"} {
		t.Run(display, func(t *testing.T) {
			s, raw, csrf := reviewAccount(t, "owner")
			w := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/runners", `{"name":"enrolled","repos":["github.com/acme/*"]}`, 201)
			var issued credentialResponse
			if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
				t.Fatal(err)
			}
			engineSQL(t, s.store, `UPDATE runs SET repo_display=? WHERE id='run'`, display)
			claim, err := s.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: issued.Prefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: "run"}})
			if display == "https://github.com/acme/repo" {
				if err != nil || claim.RunID != "run" {
					t.Fatalf("enrolled repository claim: %+v %v", claim, err)
				}
			} else if err == nil {
				t.Fatal("enrolled runner claimed another owner's repository")
			}
		})
	}
}
