package controller

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
