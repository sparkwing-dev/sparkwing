package store

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
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

// assertedRef is the record scope an asserted fixture run of ref writes.
func assertedRef(ref string) blob.RefScope {
	p, err := runspec.AssertedProvenance(fixtureWriter, ref, "", "fixture")
	if err != nil {
		panic(err)
	}
	return blob.ScopeFor(p)
}

// sealWith marshals sealed carrying p as its sealed provenance.
func sealWith(t testing.TB, sealed runspec.SealedRequest, p runspec.Provenance) []byte {
	t.Helper()
	sealed.Provenance = p
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
