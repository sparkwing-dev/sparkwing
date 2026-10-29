package store

import (
	"errors"
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func dataAccessChecker(t *testing.T, tx *storeTx) capability.DataAccessChecker {
	t.Helper()
	checker, ok := any(tx).(capability.DataAccessChecker)
	if !ok {
		t.Fatal("store transaction does not implement the data authority contract")
	}
	return checker
}

func dataAccessAuthority(t *testing.T, s *Store, prefix string) capability.Authorized {
	t.Helper()
	var p capability.Principal
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		var err error
		p, err = tx.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return capability.Authorized{Principal: p}
}

func TestBlobDataAccessTransactionSeesReceiptWritesAndDoesNotCommit(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	authority := dataAccessAuthority(t, s, actor.CapabilityPrefix)
	target := capability.DataTarget{Operation: capability.DataDownload, Digest: string(intent.Digest)}
	rollback := errors.New("rollback owning transaction")
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		checker := dataAccessChecker(t, tx)
		if err := checker.CheckDataAccess(t.Context(), authority, target); err != nil {
			return err
		}
		rewriteSchemaExec(t, tx, "DELETE FROM blob_receipts WHERE team='a' AND digest=?", intent.Digest)
		for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataHasMany, capability.DataLookup, capability.DataRecord} {
			target.Operation = operation
			if err := checker.CheckDataAccess(t.Context(), authority, target); !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatalf("uncommitted receipt removal ignored by %s: %v", operation, err)
			}
		}
		if _, err := insertBlobReceiptTx(t.Context(), tx, intent.Identity, intent.Digest); err != nil {
			return err
		}
		if err := checker.CheckDataAccess(t.Context(), authority, target); err != nil {
			return err
		}
		rewriteSchemaExec(t, tx, "DELETE FROM blob_receipts WHERE team='a' AND digest=?", intent.Digest)
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := tenant.CheckDataAccess(t.Context(), authority, target); err != nil {
		t.Fatalf("authority check committed its owner's receipt deletion: %v", err)
	}
}

func TestBlobDataAccessTransactionSeesReadEdgeWrites(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	if _, err := records.CommitRecord(t.Context(), blobOutputRequest(intent)); err != nil {
		t.Fatal(err)
	}
	reader := addBlobJob(t, s, intent, "reader")
	finishBlobRecordJob(t, records, intent)
	authority := dataAccessAuthority(t, s, reader.Authority.CapabilityRef)
	target := capability.DataTarget{Operation: capability.DataDownload, Digest: string(intent.Digest)}
	rollback := errors.New("rollback read edge")
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		checker := dataAccessChecker(t, tx)
		if err := checker.CheckDataAccess(t.Context(), authority, target); !errors.Is(err, blob.ErrDigestNotReadable) {
			t.Fatalf("unrelated job inherited a read edge: %v", err)
		}
		rewriteSchemaExec(t, tx, "UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='reader'", []byte(`{"id":"reader","deps":["job"]}`))
		if err := checker.CheckDataAccess(t.Context(), authority, target); err != nil {
			return err
		}
		rewriteSchemaExec(t, tx, "DELETE FROM blob_records WHERE team='a'")
		if err := checker.CheckDataAccess(t.Context(), authority, target); !errors.Is(err, blob.ErrDigestNotReadable) {
			t.Fatalf("removed reference still conferred authority: %v", err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := records.tenant.CheckDataAccess(t.Context(), authority, target); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("authority check committed its owner's read edge: %v", err)
	}
}

func TestBlobDataAccessTransactionAuthorityProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 4.0s measured in the short class; the full suite runs it")
	}
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	authority := dataAccessAuthority(t, s, actor.CapabilityPrefix)
	operations := []capability.DataOperation{capability.DataUpload, capability.DataCommit, capability.DataRecord, capability.DataDownload, capability.DataHasMany, capability.DataLookup}
	property := func(seed uint64, forged string) bool {
		tx, err := s.beginTx(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackOrLog(tx)
		checker := dataAccessChecker(t, tx)
		input := authority
		input.Principal.Capability.Grants = []capability.Grant{capability.DataRead, capability.DataWrite}
		input.Principal.Capability.Subject.ID = forged
		input.Principal.Capability.Binding = capability.Binding{Kind: capability.BindingNone}
		input.Principal.Capability.Data = &capability.DataScope{Use: capability.DataPublish, RepositoryKey: forged, ReadRefs: []string{forged}, WriteRef: forged}
		target := capability.DataTarget{Digest: string(intent.Digest)}
		for _, operation := range operations {
			target.Operation = operation
			if err := checker.CheckDataAccess(t.Context(), input, target); err != nil {
				t.Logf("persisted %s rights depended on supplied hints: %v", operation, err)
				return false
			}
		}
		switch seed % 5 {
		case 0:
			rewriteSchemaExec(t, tx, "UPDATE capabilities SET revoked_at=? WHERE prefix=?", s.now().UnixNano(), actor.CapabilityPrefix)
		case 1:
			rewriteSchemaExec(t, tx, "UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'")
		case 2:
			rewriteSchemaExec(t, tx, "UPDATE nodes SET lease_expires_at=? WHERE team='a' AND run_id='run' AND node_id='job'", s.now().UnixNano())
		case 3:
			input.Principal.Capability.Team = "b"
		case 4:
			rewriteSchemaExec(t, tx, "UPDATE capabilities SET revoked_at=? WHERE prefix='machine'", s.now().UnixNano())
		}
		for _, operation := range operations {
			target.Operation = operation
			if err := checker.CheckDataAccess(t.Context(), input, target); err == nil {
				t.Logf("forged principal bypassed persisted %s fence, seed=%d", operation, seed)
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(4817))}); err != nil {
		t.Fatal(err)
	}
	if err := tenant.CheckDataAccess(t.Context(), authority, capability.DataTarget{Operation: capability.DataDownload, Digest: string(intent.Digest)}); err != nil {
		t.Fatalf("rolled-back authority changes survived: %v", err)
	}
}
