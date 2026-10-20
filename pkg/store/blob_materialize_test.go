package store

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func (fakeBlobManifests) VerifyBlob(context.Context, blob.Blob) error { return nil }

func (blobManifestFunc) VerifyBlob(context.Context, blob.Blob) error { return nil }

func TestBlobMaterializationPersistsCommittedSelectors(t *testing.T) {
	_, records, change := blobResultFixture(t)
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	row, err := records.tenant.ReadNode(t.Context(), change.Ref)
	if err != nil || !reflect.DeepEqual(row.OutputRecord, change.Extra.Result.OutputRecord) || !reflect.DeepEqual(row.ManifestRecord, change.Extra.Result.ManifestRecord) {
		t.Fatalf("committed selectors lost: %+v: %v", row, err)
	}
}

func blobMaterializationFixture(t *testing.T) (*Store, *Tenant, ReconcileRequest) {
	t.Helper()
	s, records, change := blobResultFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_hash='same'")
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	s.SetResultBlobRecords(records.manifests)
	return materializationTarget(t, s, records.tenant, change.Ref)
}

func materializationTarget(t *testing.T, s *Store, tenant *Tenant, sourceRef NodeRef) (*Store, *Tenant, ReconcileRequest) {
	t.Helper()
	maintenanceRun(t, s, "target", 2, RunRunning)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET pipeline='ci',repo_key='gh:1',request_json=? WHERE id='target'", []byte(`{"version":1,"request":{"git":{"ref":"main"}},"provenance":{"trust":"asserted","ref":"main","pipeline":"ci","writer":"account/fixture"}}`))
	rewriteSchemaExec(t, s.db, "UPDATE runs SET lineage_json=? WHERE id='target'", []byte(`{"retry_of":"run","retry":{"mode":"failed","results":{"job":{"Ref":{"run_id":"run","job_id":"job"},"StateSeq":1}}}}`))
	rewriteSchemaExec(t, s.db, "INSERT INTO nodes(team,run_id,node_id,status,spec_json,spec_hash,execution_json) VALUES ('a','target','job','pending',?,'same',?)", []byte(`{"id":"job","memo":{}}`), []byte(`{}`))
	owner := authorityRoot("target-owner", "a")
	owner.Subject.ID = "host"
	owner.Grants = []capability.Grant{capability.TriggerHost, capability.DataRead, capability.DataWrite, capability.LogTail}
	owner.Unit, owner.UnitID = capability.UnitTrigger, "target"
	owner.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "machine"}
	owner.Purpose, owner.Delivery, owner.Issuance = capability.PurposeTrigger, capability.DeliveryDirect, capability.Award
	owner.Billing = &capability.BillingProvenance{PayerTeam: "a", ReservationID: "target-owner", ExecutorClass: "customer", Unit: capability.BillingHost, Start: capability.BillingAtAward}
	owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "target", Holder: "host", Generation: 1}
	owner.Data = &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "gh:1", RunID: "target", WriteRef: assertedRef("main").CapabilityScope(), ReadRefs: []string{assertedRef("main").CapabilityScope()}}
	expiry := s.now().Add(time.Hour)
	owner.ExpiresAt = &expiry
	authorityPersist(t, s, owner)
	rewriteSchemaExec(t, s.db, "INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels) VALUES ('a','target','claimed','trigger',1,1,'host','target-owner',1,?,?,?)", expiry.UnixNano(), []byte(`[]`), []byte(`[]`))
	actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: owner.Prefix}, CapabilityPrefix: owner.Prefix, Owner: OwnerLeaseRef{Kind: OwnerTrigger, RunID: "target", Holder: "host", Generation: 1}}
	source := MaterializeSource{Kind: SourceManualRetry, Result: &MaterializedResultSource{Ref: sourceRef, StateSeq: 1}}
	return s, tenant, ReconcileRequest{Ref: NodeRef{"target", "job"}, Actor: actor, Policy: source.Kind, Source: &source}
}

func MaterializationTestFixture(t *testing.T, manifests BlobManifestReader, output, manifest blob.Blob, members []blob.Blob) (*Store, *Tenant, ReconcileRequest) {
	t.Helper()
	s, records, intent := blobRecordFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_hash='same'")
	records.manifests = manifests
	s.SetResultBlobRecords(manifests)
	for _, object := range append([]blob.Blob{output, manifest}, members...) {
		intent.Digest, intent.Size = object.Digest, object.Size
		if _, err := commitTestBlob(t, records.tenant, intent); err != nil {
			t.Fatal(err)
		}
	}
	intent.Digest, intent.Size = output.Digest, output.Size
	out := blobOutputRequest(intent)
	if _, err := records.CommitRecord(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	intent.Digest, intent.Size = manifest.Digest, manifest.Size
	artifacts := blobOutputRequest(intent)
	artifacts.Key.Kind = blob.KindManifest
	var err error
	artifacts.Members, err = manifests.ReadBlobManifest(t.Context(), records.tenant.team, manifest.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CommitRecord(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	memo := out
	memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
	memo.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: manifest.Digest}
	memo.Members = artifacts.Members
	if _, err := records.CommitRecord(t.Context(), memo); err != nil {
		t.Fatal(err)
	}
	attempt := intent.Identity.Attempt.Value
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: intent.Authority.CapabilityRef}, CapabilityPrefix: intent.Authority.CapabilityRef, Holder: "machine", Generation: attempt.Generation}
	ref := NodeRef{attempt.RunID, attempt.NodeID}
	change := NodeTransition{Ref: ref, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(attempt.Attempt), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeSuccess, OutputRecord: &out.Key, ManifestRecord: &artifacts.Key}}}
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	s, tenant, request := materializationTarget(t, s, records.tenant, ref)
	request.Policy, request.Source = SourceMemo, &MaterializeSource{Kind: SourceMemo, Record: memo.Key}
	return s, tenant, request
}

func TestBlobMaterializationCopiesExactRecordsWithoutAnAttempt(t *testing.T) {
	s, tenant, request := blobMaterializationFixture(t)
	got, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || got.Kind != Materialize || got.Node.Attempt != 0 || got.Node.OutputRecord == nil || got.Node.ManifestRecord == nil {
		t.Fatalf("materialization: %+v: %v", got, err)
	}
	rewriteSchemaCount(t, s.db, "blob_records", 4)
	rewriteSchemaCount(t, s.db, "node_attempts", 1)
	if _, err := s.db.Exec("DELETE FROM runs WHERE team='a' AND id='run'"); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "blob_records", 2)
	request.Source, request.Policy = nil, SourceNone
	if got, err := tenant.ReconcileNode(t.Context(), request); err != nil || got.Kind != KeepTerminal || got.Node.OutputRecord == nil {
		t.Fatalf("replay after source retention: %+v: %v", got, err)
	}
}

func TestBlobMaterializationSourceScopeProperty(t *testing.T) {
	s, tenant, request := blobMaterializationFixture(t)
	property := func(value uint32, field uint8) bool {
		tx, err := s.beginTx(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackOrLog(tx)
		switch field % 5 {
		case 0:
			rewriteSchemaExec(t, tx, "UPDATE runs SET repo_key=? WHERE id='run'", "other")
		case 1:
			rewriteSchemaExec(t, tx, "UPDATE runs SET pipeline=? WHERE id='run'", "other")
		case 2:
			rewriteSchemaExec(t, tx, "UPDATE nodes SET state_seq=? WHERE run_id='run'", int64(value)+2)
		case 3:
			rewriteSchemaExec(t, tx, "UPDATE runs SET request_json=? WHERE id='run'", []byte(`{"version":1,"request":{"git":{"ref":"other"}},"provenance":{"trust":"asserted","ref":"other","pipeline":"ci","writer":"account/fixture"}}`))
		case 4:
			rewriteSchemaExec(t, tx, "UPDATE capabilities SET revoked_at=1 WHERE prefix='target-owner'")
		}
		_, err = transitionNodeTx(t.Context(), tx, tenant.team, NodeTransition{Ref: request.Ref, Actor: request.Actor, Cause: CauseMaterialize, Extra: TransitionExtra{Materialize: request.Source}}, tx.now())
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 80, Rand: rand.New(rand.NewSource(701))}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "blob_records", 2)
}

func TestBlobMaterializationRollsBackBothRecordsWithState(t *testing.T) {
	s, tenant, request := blobMaterializationFixture(t)
	rewriteSchemaExec(t, s.db, "CREATE TRIGGER refuse_copy_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'event unavailable'); END")
	if _, err := tenant.ReconcileNode(t.Context(), request); err == nil {
		t.Fatal("copy survived failed state commit")
	}
	rewriteSchemaCount(t, s.db, "blob_records", 2)
	row, err := tenant.ReadNode(t.Context(), request.Ref)
	if err != nil || row.Status != StatusPending || row.OutputRecord != nil || row.ManifestRecord != nil {
		t.Fatalf("partial result: %+v: %v", row, err)
	}
}

func TestBlobMaterializationCannotOutliveOwnerProperty(t *testing.T) {
	s, tenant, request := blobMaterializationFixture(t)
	records := s.materializations.(*blobRecords)
	manifests := records.manifests
	base := s.now()
	property := func(remaining, delay uint16) bool {
		now := base
		s.clock = func() time.Time { return now }
		expiry := base.Add((time.Duration(remaining) + 1) * time.Nanosecond)
		rewriteSchemaExec(t, s.db, "UPDATE triggers SET lease_expires_at=? WHERE id='target'", expiry.UnixNano())
		records.manifests = blobManifestFunc(func(ctx context.Context, team Team, digest blob.Digest) ([]blob.Member, error) {
			now = expiry.Add(time.Duration(delay) * time.Nanosecond)
			return manifests.ReadBlobManifest(ctx, team, digest)
		})
		_, err := tenant.ReconcileNode(t.Context(), request)
		rewriteSchemaCount(t, s.db, "blob_records", 2)
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(709))}); err != nil {
		t.Fatal(err)
	}
}

func TestBlobMaterializationMemoSelectsExactRecord(t *testing.T) {
	s, tenant, request := blobMaterializationFixture(t)
	request.Policy, request.Source = SourceMemo, seedMaterializationMemo(t, s, tenant)
	got, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || got.Node.Outcome != NodeMemoized || got.Node.OutputRecord == nil || got.Node.ManifestRecord == nil || got.Node.Attempt != 0 {
		t.Fatalf("memo copy: %+v: %v", got, err)
	}
}

func seedMaterializationMemo(t *testing.T, s *Store, tenant *Tenant) *MaterializeSource {
	t.Helper()
	source, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
	if err != nil {
		t.Fatal(err)
	}
	key := *source.OutputRecord
	key.Kind, key.Name, key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
	namespace, err := blob.RecordNamespace(key)
	if err != nil {
		t.Fatal(err)
	}
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		originalKey := *source.OutputRecord
		originalScope, err := blob.RecordNamespace(originalKey)
		if err != nil {
			return err
		}
		record, _, err := readBlobRecordTx(t.Context(), tx, originalKey, originalScope)
		if err != nil {
			return err
		}
		record.Key = key
		record.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
		return persistBlobRecordTx(t.Context(), tx, record, namespace, record.Source, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	return &MaterializeSource{Kind: SourceMemo, Record: key}
}

func TestBlobMaterializationCoalescingRequiresPersistedReadAuthority(t *testing.T) {
	_, tenant, request := blobMaterializationFixture(t)
	request.Policy, request.Source.Kind = SourceCoalesced, SourceCoalesced
	if _, err := tenant.ReconcileNode(t.Context(), request); err == nil {
		t.Fatal("spec equality authorized an unproven coalesced read")
	}
}

func TestBlobMaterializationMissLeavesJobAvailable(t *testing.T) {
	for _, missing := range []string{"output", "manifest", "member"} {
		t.Run(missing, func(t *testing.T) {
			s, tenant, request := blobMaterializationFixture(t)
			request.MaterializeOnly = true
			if missing == "member" {
				rewriteSchemaExec(t, s.db, "DELETE FROM blob_members")
			} else {
				rewriteSchemaExec(t, s.db, "DELETE FROM blob_records WHERE kind=?", missing)
			}
			got, err := tenant.ReconcileNode(t.Context(), request)
			if err != nil || got.Kind != Dispatch || got.Node.Status != StatusPending || got.Node.StateSeq != 0 || got.Node.OutputRecord != nil || got.Claim != nil {
				t.Fatalf("miss was not executable: %+v: %v", got, err)
			}
			var copied int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM blob_records WHERE team='a' AND run_id='target'").Scan(&copied); err != nil || copied != 0 {
				t.Fatalf("miss retained copies: %d: %v", copied, err)
			}
		})
	}
}

func TestBlobMaterializationReadSelectorsCannotBeAliasedProperty(t *testing.T) {
	s, tenant, request := blobMaterializationFixture(t)
	result, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	property := func(value uint64) bool {
		tx, err := s.beginTx(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackOrLog(tx)
		a, err := resolveBlobAuthorityTx(t.Context(), tx, tenant.team, blob.Authority{CapabilityRef: request.Actor.CapabilityPrefix}, capability.DataRead)
		if err != nil {
			t.Fatal(err)
		}
		key := *result.Node.OutputRecord
		scope, err := blob.RecordNamespace(key)
		if err != nil {
			t.Fatal(err)
		}
		record, _, err := readBlobRecordTx(t.Context(), tx, key, scope)
		if err != nil {
			t.Fatal(err)
		}
		key.Scope.Key = fmt.Sprintf("unselected/%d", value)
		record.Key = key
		scope, err = blob.RecordNamespace(key)
		if err != nil {
			t.Fatal(err)
		}
		if err := persistBlobRecordTx(t.Context(), tx, record, scope, a.source, &result.Node); err != nil {
			t.Fatal(err)
		}
		records := &blobRecords{tenant: tenant}
		hit, err := records.lookupRecordTx(t.Context(), tx, a, blob.RecordLookup{Key: key})
		return err != nil && !hit.Found
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(719))}); err != nil {
		t.Fatal(err)
	}
}
