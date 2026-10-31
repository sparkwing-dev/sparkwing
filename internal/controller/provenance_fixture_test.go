package controller

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// fixtureWriter is the principal every asserted fixture run was submitted by.
var fixtureWriter = runspec.PrincipalWriter("account", "fixture")

// assertedProvenance is what intake seals for a run the fixture account submitted.
func assertedProvenance(t testing.TB, pipeline, ref, sha string) runspec.Provenance {
	t.Helper()
	p, err := runspec.AssertedProvenance(fixtureWriter, ref, sha, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// appProvenance is what intake seals for a signed App delivery of app.
func appProvenance(t testing.TB, app runspec.AppFacts, pr runspec.Optional[runspec.PullRequest], pipeline string) runspec.Provenance {
	t.Helper()
	p, err := runspec.AppProvenance(app, pr, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// withProvenance returns sealed carrying p as its sealed provenance.
func withProvenance(sealed runspec.SealedRequest, p runspec.Provenance) runspec.SealedRequest {
	sealed.Provenance = p
	return sealed
}

// sealWith marshals sealed carrying p as its sealed provenance.
func sealWith(t testing.TB, sealed runspec.SealedRequest, p runspec.Provenance) []byte {
	t.Helper()
	body, err := json.Marshal(withProvenance(sealed, p))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func fetchedRunFixture(t *testing.T, s *store.Store, team store.Team, runID, repository string) {
	t.Helper()
	tenant, err := s.ForTeam(t.Context(), team)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	request := row.Request.Request
	request.Git.Repository = runspec.Repository{Key: "gh:1", Display: "https://github.com/" + repository}
	request.Source.Location.RemoteURL = request.Git.Repository.Display
	proof, err := row.Request.Provenance.FetchedFrom(1, repository, "", "")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := runspec.SealRequest(request, runspec.IntakeFacts{RunID: row.ID, AuthorityRef: row.Request.AuthorityRef, AdmittedAt: row.Request.AdmittedAt, Provenance: proof})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s, `UPDATE runs SET request_json=?,request_hash=?,repo_key='gh:1',repo_display=?,repo_identity=? WHERE team=? AND id=?`, body, sealed.Hash, request.Git.Repository.Display, "github.com/"+repository, team, runID)
}
