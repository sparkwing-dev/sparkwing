package store

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// fixtureProvenance is what intake seals for a run an account submitted.
func fixtureProvenance(t testing.TB, pipeline, ref string) runspec.Provenance {
	t.Helper()
	p, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "fixture"), ref, "", pipeline)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fixtureRef is the record scope fixtureProvenance yields for ref.
func fixtureRef(ref string) blob.RefScope {
	p, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "fixture"), ref, "", "fixture")
	if err != nil {
		panic(err)
	}
	return blob.ScopeFor(p)
}

// fixtureSeal marshals sealed with the provenance intake would have sealed for a
// run of pipeline: verified when it carries App facts, asserted otherwise.
func fixtureSeal(t testing.TB, pipeline string, sealed runspec.SealedRequest) []byte {
	t.Helper()
	if !sealed.Provenance.Valid() {
		git := sealed.Request.Git
		if git.App.Set {
			p, err := runspec.AppProvenance(git.App.Value, sealed.Request.PullRequest, pipeline)
			if err != nil {
				t.Fatal(err)
			}
			sealed.Provenance = p
		} else {
			sha := ""
			if runspec.IsCommitID(git.SHA) {
				sha = git.SHA
			}
			p, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "fixture"), git.Ref, sha, pipeline)
			if err != nil {
				t.Fatal(err)
			}
			sealed.Provenance = p
		}
	}
	body, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// fixtureFeatureSHA is the full commit a verified fixture delivery names.
const fixtureFeatureSHA = "fea7fea7fea7fea7fea7fea7fea7fea7fea7fea7"

// fixtureOtherSHA is a full commit distinct from fixtureFeatureSHA.
const fixtureOtherSHA = "0123456789abcdef0123456789abcdef01234567"
