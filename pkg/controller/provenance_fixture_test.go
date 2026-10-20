package controller

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// fixtureWriter is the principal every asserted fixture run was submitted by.
var fixtureWriter = runspec.PrincipalWriter("account", "fixture")

// requestProvenance is the provenance intake would seal for request as a run of
// pipeline: verified for an App-origin request carrying verified App facts,
// asserted by the fixture account otherwise.
func requestProvenance(t testing.TB, pipeline string, request runspec.Request) runspec.Provenance {
	t.Helper()
	git := request.Git
	var (
		p   runspec.Provenance
		err error
	)
	if request.Origin == runspec.OriginApp && git.App.Set && !git.App.Value.VerifiedAt.IsZero() {
		p, err = runspec.AppProvenance(git.App.Value, request.PullRequest, pipeline)
	} else {
		sha := ""
		if runspec.IsCommitID(git.SHA) {
			sha = git.SHA
		}
		p, err = runspec.AssertedProvenance(fixtureWriter, git.Ref, sha, pipeline)
	}
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// sealFixture fills in the provenance intake would have sealed, unless the
// fixture already sealed one.
func sealFixture(t testing.TB, pipeline string, sealed runspec.SealedRequest) runspec.SealedRequest {
	t.Helper()
	if !sealed.Provenance.Valid() {
		sealed.Provenance = requestProvenance(t, pipeline, sealed.Request)
	}
	return sealed
}

// fixtureSeal marshals sealFixture's result.
func fixtureSeal(t testing.TB, pipeline string, sealed runspec.SealedRequest) []byte {
	t.Helper()
	body, err := json.Marshal(sealFixture(t, pipeline, sealed))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// fixtureReseal marshals sealed with the provenance intake would seal for its
// current request, after a fixture rewrote the facts a stored seal was made from.
func fixtureReseal(t testing.TB, pipeline string, sealed runspec.SealedRequest) []byte {
	t.Helper()
	sealed.Provenance = runspec.Provenance{}
	return fixtureSeal(t, pipeline, sealed)
}
