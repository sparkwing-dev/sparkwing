package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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

func TestEnrollmentPlatformControlsVerifiedMainClaims(t *testing.T) {
	for _, platform := range []string{"", "kubernetes"} {
		t.Run(platform, func(t *testing.T) {
			s, raw, csrf := reviewAccount(t, "owner")
			body, err := json.Marshal(runnerEnrollmentRequest{Name: "enrolled", Repos: []string{"github.com/acme/*"}, Platform: platform})
			if err != nil {
				t.Fatal(err)
			}
			w := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/runners", string(body), 201)
			var issued credentialResponse
			if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
				t.Fatal(err)
			}
			provenance, err := runspec.AppProvenance(runspec.AppFacts{Repository: "acme/repo", Event: "push", RepositoryID: 1, DeliveryKey: "delivery", VerifiedAt: time.Now(), Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), DefaultBranch: "main", DefaultBranchPush: true}, runspec.Optional[runspec.PullRequest]{}, "ci")
			if err != nil {
				t.Fatal(err)
			}
			body, err = json.Marshal(runspec.SealedRequest{Provenance: provenance})
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, s.store, `UPDATE runs SET request_json=?,repo_display='https://github.com/acme/repo' WHERE id='run'`, body)
			_, err = s.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: issued.Prefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: "run"}})
			if (err == nil) != (platform == "") {
				t.Fatalf("enrolled platform %q claim: %v", platform, err)
			}
		})
	}
}
