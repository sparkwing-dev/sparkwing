package store

import (
	"errors"
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestBlobDataAccessLogicalSelection(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	authority := dataAccessAuthority(t, s, intent.Authority.CapabilityRef)
	target := capability.DataTarget{Operation: capability.DataLookup, RecordKind: string(blob.KindMemo), RecordName: "input-hash"}
	if err := records.tenant.CheckDataAccess(t.Context(), authority, target); err != nil {
		t.Fatalf("logical lookup before selecting a digest: %v", err)
	}
	target.Operation = capability.DataRecord
	if err := records.tenant.CheckDataAccess(t.Context(), authority, target); err != nil {
		t.Fatalf("memo copy before selecting a hit: %v", err)
	}
	for _, operation := range []capability.DataOperation{capability.DataUpload, capability.DataCommit, capability.DataDownload, capability.DataHasMany} {
		target.Operation = operation
		if err := records.tenant.CheckDataAccess(t.Context(), authority, target); err == nil {
			t.Fatalf("%s accepted no digest", operation)
		}
	}
	target.Operation, target.RecordKind = capability.DataRecord, string(blob.KindOutput)
	if err := records.tenant.CheckDataAccess(t.Context(), authority, target); err == nil {
		t.Fatal("record publication accepted no digest")
	}
	target.Operation, target.RecordKind = capability.DataLookup, "log"
	if err := records.tenant.CheckDataAccess(t.Context(), authority, target); err == nil {
		t.Fatal("logical lookup accepted logs")
	}
	other := addBlobJob(t, s, intent, "unrelated")
	if err := records.tenant.CheckDataAccess(t.Context(), authority, capability.DataTarget{Operation: capability.DataLookup, RecordKind: "output", RecordName: "unrelated"}); err != nil {
		t.Fatalf("selection authority incorrectly depends on record existence: %v", err)
	}
	if _, err := commitTestBlob(t, records.tenant, other); err != nil {
		t.Fatal(err)
	}
	if _, err := records.CommitRecord(t.Context(), blobOutputRequest(other)); err != nil {
		t.Fatal(err)
	}
	if _, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: blobOutputRequest(other).Key}); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("selection preflight widened the record read set: %v", err)
	}
}

func TestBlobDataAccessLogicalSelectionAuthorityProperty(t *testing.T) {
	s, records, _ := blobRecordFixture(t)
	reader := authorityRoot("remote-cache", "a")
	reader.Grants = []capability.Grant{capability.DataRead}
	reader.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
	authorityPersist(t, s, reader)
	authority := dataAccessAuthority(t, s, reader.Prefix)
	property := func(seed uint64, name string) bool {
		tx, err := s.beginTx(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackOrLog(tx)
		target := capability.DataTarget{Operation: capability.DataLookup, RecordKind: "memo", RecordName: "hash:" + name}
		input := authority
		input.Principal.Capability.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: name, ReadRefs: []string{name}, VerifiedDefaultOnly: false}
		if err := tx.CheckDataAccess(t.Context(), input, target); err != nil {
			t.Logf("persisted selector authority depended on hints: %v", err)
			return false
		}
		switch seed % 6 {
		case 0:
			rewriteSchemaExec(t, tx, "UPDATE capabilities SET revoked_at=? WHERE prefix=?", s.now().UnixNano(), reader.Prefix)
		case 1:
			rewriteSchemaExec(t, tx, "UPDATE capabilities SET expires_at=? WHERE prefix=?", s.now().UnixNano(), reader.Prefix)
		case 2:
			input.Principal.Capability.Team = "b"
		case 3:
			target.RecordKind = "binary"
		case 4:
			target.RecordKind = "source_bundle"
		case 5:
			target.Operation = capability.DataRecord
		}
		if err := tx.CheckDataAccess(t.Context(), input, target); err == nil {
			t.Logf("selector bypassed persisted authority: seed=%d", seed)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(9147))}); err != nil {
		t.Fatal(err)
	}
	if err := records.tenant.CheckDataAccess(t.Context(), authority, capability.DataTarget{Operation: capability.DataLookup, RecordKind: "memo", RecordName: "absent"}); err != nil {
		t.Fatalf("rolled-back authority changes survived: %v", err)
	}
}
