package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// TestOIDCRepositoryClaimReadsSealedProvenance keeps the repository claim on the
// name GitHub reported at intake, whatever the submitted Git text says later.
func TestOIDCRepositoryClaimReadsSealedProvenance(t *testing.T) {
	sha := strings.Repeat("b", 40)
	app, err := runspec.AppProvenance(runspec.AppFacts{Event: "push", RepositoryID: 9, Repository: "acme/real", Ref: "refs/heads/main", SHA: sha, DefaultBranch: "main", DefaultBranchPush: true, DeliveryKey: "delivery", VerifiedAt: time.Unix(1, 0)}, runspec.Optional[runspec.PullRequest]{}, "ci")
	if err != nil {
		t.Fatal(err)
	}
	asserted, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "a"), "refs/heads/main", sha, "ci")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		provenance runspec.Provenance
		want       string
	}{{app, "acme/real"}, {asserted, ""}} {
		var row RunRow
		row.ID = "run"
		row.Request.Provenance = tc.provenance
		row.Request.Request.Git.Repository.Display = "acme/forged"
		facts, err := oidcIdentity("team", row)
		if err != nil {
			t.Fatal(err)
		}
		if facts.Repository != tc.want {
			t.Fatalf("repository claim = %q, want %q", facts.Repository, tc.want)
		}
	}
	var forged runspec.Provenance
	if err := json.Unmarshal([]byte(`{"trust":"asserted","ref":"refs/heads/main","pipeline":"ci","writer":"account/a","repository":"acme/real"}`), &forged); err == nil {
		t.Fatal("an asserted seal named a repository")
	}
}
