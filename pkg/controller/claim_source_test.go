package controller_test

import (
	"crypto/sha256"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (f *appFixture) launchedRun(who signedIn, runID, owner, name string) string {
	f.t.Helper()
	id := map[string]int64{"acme/widgets": 701, "acme/plans": 702}[owner+"/"+name]
	seedSourceRun(f.t, f.store, who.team, runID, "https://github.com/"+owner+"/"+name+".git", githubFixtureKey(id), "refs/heads/main", headSHA, id, runspec.DispatchController)
	root, err := f.store.LookupCapability(f.t.Context(), sha256.Sum256([]byte(f.admin)))
	if err != nil {
		f.t.Fatal(err)
	}
	launcher, err := f.store.ProvisionCapability(f.t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionService, Authority: &root.Authority, RegistrationID: "service:launcher-" + runID, Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher-" + runID}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}})
	if err != nil {
		f.t.Fatal(err)
	}
	tenant, err := f.store.ForTeam(f.t.Context(), store.Team(who.team))
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := tenant.RecordCreditGrant(f.t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100000000, Reference: "fixture:" + runID, CreatedBy: "fixture"}); err != nil {
		f.t.Fatal(err)
	}
	claim, err := f.store.ClaimUnit(f.t.Context(), store.UnitDemand{CapabilityPrefix: launcher.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: runID}})
	if err != nil {
		f.t.Fatal(err)
	}
	return string(claim.Bearer)
}

// The token reads the repositories an owner approved by their GitHub IDs, so
// renaming an approved repository and giving its old name to another one
// changes nothing it reaches.
func TestRunSourceCredential_MintsTheApprovedRepositoriesByID(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code, out := f.setExtraRepos(olga, "acme/widgets", []string{"acme/plans"}); code != http.StatusOK {
		t.Fatalf("extra repos = %d %v", code, out)
	}
	f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: "acme/widgets"},
		githubapptest.Repo{ID: 702, FullName: "acme/plans-archive", Private: true},
		githubapptest.Repo{ID: 703, FullName: "acme/plans", Private: true})
	tok := f.launchedRun(olga, "run-renamed", "acme", "widgets")
	var sc controller.SourceCredentialResponse
	if code := f.call("POST", "/api/v1/runs/run-renamed/source-credential", "Bearer "+tok, nil, &sc); code != http.StatusOK {
		t.Fatalf("source credential = %d", code)
	}
	if !f.app.TokenCovers(sc.Secret, "acme/plans-archive") || f.app.TokenCovers(sc.Secret, "acme/plans") {
		t.Fatal("the token followed the name to another repository")
	}
}

// A repository no installation of the team covers gets no credential: Cloud
// fetches only through the GitHub App.
func TestRunSourceCredential_NeedsTheGitHubApp(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	tok := f.launchedRun(olga, "run-bare", "acme", "widgets")
	var out map[string]any
	if code := f.call("POST", "/api/v1/runs/run-bare/source-credential", "Bearer "+tok, nil, &out); code != http.StatusNotFound {
		t.Fatalf("uncovered = %d %v, want 404", code, out)
	}
}
