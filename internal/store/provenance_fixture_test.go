package store

import (
	"encoding/json"
	"testing"
	"time"

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

func fixtureRunWriter(t *testing.T, s *Store, run string, writer runspec.Writer) {
	t.Helper()
	var body []byte
	if err := s.queryRow(t.Context(), `SELECT request_json FROM runs WHERE team='a' AND id=?`, run).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(body, &sealed); err != nil {
		t.Fatal(err)
	}
	var err error
	sealed.Provenance, err = runspec.AssertedProvenance(writer, sealed.Provenance.Ref(), sealed.Provenance.SHA(), sealed.Provenance.Pipeline())
	if err != nil {
		t.Fatal(err)
	}
	sealed.Hash, err = runspec.CanonicalHash(sealed)
	if err != nil {
		t.Fatal(err)
	}
	s.testOnlyExec(t, `UPDATE runs SET request_json=? WHERE team='a' AND id=?`, sealWith(t, sealed, sealed.Provenance), run)
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

func testAllowedPools(t *testing.T, s *Store, team Team, provenance runspec.Provenance) []string {
	t.Helper()
	var pools []string
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		var err error
		pools, _, err = runPoolsTx(t.Context(), tx, team, provenance)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return pools
}

func grantFixturePool(t *testing.T, s *Store, run, pool, branch string) {
	t.Helper()
	var body []byte
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return tx.QueryRowContext(t.Context(), `SELECT request_json FROM runs WHERE id=?`, run).Scan(&body)
	}); err != nil {
		t.Fatal(err)
	}
	var seal runspec.SealedRequest
	if err := json.Unmarshal(body, &seal); err != nil {
		t.Fatal(err)
	}
	pipeline := "ci"
	if seal.Provenance.Valid() {
		pipeline = seal.Provenance.Pipeline()
	}
	ref := "refs/heads/" + branch
	p, err := runspec.AppProvenance(runspec.AppFacts{Repository: "acme/repo", Event: "push", RepositoryID: 1, DeliveryKey: "pool-delivery", VerifiedAt: time.Unix(1, 0).UTC(), Ref: ref, SHA: referenceMain, DefaultBranch: "main", DefaultBranchPush: branch == "main"}, runspec.Optional[runspec.PullRequest]{}, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	s.testOnlyExec(t, `UPDATE runs SET request_json=? WHERE id=?`, sealWith(t, seal, p), run)
	s.testOnlyExec(t, `INSERT INTO pool_access_rules(team,id,pool,repo_key,created_by,created_at) VALUES ('a',?,?,'gh:1','owner',1)`, pool, pool)
}
