package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestBlobRecordsVerifiedDefaultLookupFiltersPersistedEvidence(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	request := blobOutputRequest(intent)
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	lookup := blob.RecordLookup{Authority: intent.Authority, Key: request.Key, VerifiedDefaultOnly: true}
	for _, tc := range []struct {
		name  string
		facts blob.RefFacts
	}{
		{name: "manual"},
		{name: "verified feature", facts: blob.RefFacts{Scope: blob.RefScope{Ref: "feature", Verified: true}, SHA: "sha", AppDeliveryKey: "delivery", DefaultBranch: "main"}},
		{name: "forged default flag", facts: blob.RefFacts{Scope: blob.RefScope{Ref: "main"}, SHA: "sha", AppDeliveryKey: "delivery", DefaultBranch: "main", DefaultBranchVerified: true}},
		{name: "missing event evidence", facts: blob.RefFacts{Scope: blob.RefScope{Ref: "main", Verified: true}, DefaultBranch: "main", DefaultBranchVerified: true}},
		{name: "verified facts in manual scope", facts: blob.RefFacts{Scope: blob.RefScope{Ref: "main", Verified: true}, SHA: "sha", AppDeliveryKey: "delivery", DefaultBranch: "main", DefaultBranchVerified: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts, err := json.Marshal(tc.facts)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE blob_records SET ref_facts_json=?", facts)
			got, err := records.LookupRecord(t.Context(), lookup)
			if err != nil || got.Found {
				t.Fatalf("restricted lookup accepted invalid evidence: %+v: %v", got, err)
			}
		})
	}
	sealed := sealWith(t, runspec.SealedRequest{Version: 1, Request: runspec.Request{Git: runspec.Git{Ref: "main", DefaultBranch: runspec.Optional[string]{Set: true, Value: "trunk"}}}}, assertedProvenance(t, "ci", "main", ""))
	rewriteSchemaExec(t, s.db, "UPDATE runs SET request_json=? WHERE id='run'", sealed)
	lookup.Authority.Ref = blob.RefFacts{}
	if hit, err := records.LookupRecord(t.Context(), lookup); err != nil || hit.Found {
		t.Fatalf("lookup ignored sealed default branch: %+v: %v", hit, err)
	}
	lookup.VerifiedDefaultOnly = false
	if hit, err := records.LookupRecord(t.Context(), lookup); err != nil || !hit.Found {
		t.Fatalf("ordinary authorized record was filtered: %+v: %v", hit, err)
	}
}

func TestBlobRecordsVerifiedDefaultMissDoesNotReadRequiredBytes(t *testing.T) {
	_, records, intent := blobRecordFixture(t)
	manifests := fakeBlobManifests{intent.Digest: nil}
	records.manifests = manifests
	request := blobOutputRequest(intent)
	request.ManifestDigest.Set, request.ManifestDigest.Value = true, intent.Digest
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	delete(manifests, intent.Digest)
	lookup := blob.RecordLookup{Authority: intent.Authority, Key: request.Key, VerifiedDefaultOnly: true}
	if hit, err := records.LookupRecord(t.Context(), lookup); err != nil || hit.Found {
		t.Fatalf("excluded record was read: %+v: %v", hit, err)
	}
	lookup.VerifiedDefaultOnly = false
	if _, err := records.LookupRecord(t.Context(), lookup); err == nil {
		t.Fatal("required ordinary lookup ignored missing bytes")
	}
}

func TestBlobRecordsRemoteCacheCannotWeakenVerifiedDefaultPolicy(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	request := blobOutputRequest(intent)
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	key := request.Key
	key.Scope.Ref = blob.RefScope{Ref: "main", Verified: true}
	scope, err := blob.RecordNamespace(key)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE blob_records SET scope=?,ref_scope='main'", scope)
	reader := authorityRoot("remote-cache", "a")
	reader.Grants = []capability.Grant{capability.DataRead}
	reader.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
	authorityPersist(t, s, reader)
	lookup := blob.RecordLookup{Authority: blob.Authority{CapabilityRef: reader.Prefix}, Key: key}
	for _, verified := range []bool{false, true} {
		facts, err := json.Marshal(blob.RefFacts{Scope: blob.RefScope{Ref: "main", Verified: verified}, DefaultBranch: "main", DefaultBranchVerified: verified, SHA: "sha", AppDeliveryKey: "delivery"})
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, "UPDATE blob_records SET ref_facts_json=?", facts)
		for _, flag := range []bool{false, true} {
			lookup.VerifiedDefaultOnly = flag
			hit, err := records.LookupRecord(t.Context(), lookup)
			if hit.Found != verified || (err != nil && !errors.Is(err, blob.ErrDigestNotReadable)) || (verified && err != nil) {
				t.Fatalf("verified=%v, flag=%v: found=%v: %v", verified, flag, hit.Found, err)
			}
		}
	}
}
