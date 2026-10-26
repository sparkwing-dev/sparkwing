package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
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
	{
		records := &blobRecords{manifests: records.manifests}
		s.resultRecords, s.materializations = records, records
	}
	return materializationTarget(t, s, records.tenant, change.Ref)
}

func materializationTarget(t *testing.T, s *Store, tenant *Tenant, sourceRef runspec.NodeRef) (*Store, *Tenant, ReconcileRequest) {
	t.Helper()
	maintenanceRun(t, s, "target", 2, runstate.RunRunning)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET pipeline='ci',repo_key='gh:1',request_json=?,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan',sealed_at=1,plan_accepted_generation=1 WHERE id='target'", []byte(`{"version":1,"request":{"git":{"ref":"main"}},"provenance":{"trust":"asserted","ref":"main","pipeline":"ci","writer":"account/fixture"}}`), []byte(`{}`), []byte(`{"jobs":[{"id":"job","memo":{}}]}`))
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
	rewriteSchemaExec(t, s.db, "INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels,claimed_at) VALUES ('a','target','claimed','trigger',1,1,'host','target-owner',1,?,?,?,1)", expiry.UnixNano(), []byte(`[]`), []byte(`[]`))
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: owner.Prefix}, CapabilityPrefix: owner.Prefix, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: "target", Holder: "host", Generation: 1}}}
	source := runstate.MaterializeSource{Kind: runstate.SourceManualRetry, Result: &runstate.MaterializedResultSource{Ref: sourceRef, StateSeq: 1}}
	return s, tenant, ReconcileRequest{Ref: runspec.NodeRef{RunID: "target", NodeID: "job"}, Actor: actor, Policy: source.Kind, Source: &source}
}

func MaterializationTestFixture(t *testing.T, manifests BlobManifestReader, output, manifest blob.Blob, members []blob.Blob) (*Store, *Tenant, ReconcileRequest) {
	t.Helper()
	s, records, intent := blobRecordFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_hash='same'")
	records.manifests = manifests
	{
		records := &blobRecords{manifests: manifests}
		s.resultRecords, s.materializations = records, records
	}
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
	attempt := intent.Identity.Attempt.Value
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: intent.Authority.CapabilityRef}, CapabilityPrefix: intent.Authority.CapabilityRef, NodeFence: runspec.NodeFence{RunID: runspec.RunID(attempt.RunID), NodeID: runspec.NodeID(attempt.NodeID), Holder: "machine", Generation: runspec.ClaimGeneration(attempt.Generation)}}
	ref := runspec.NodeRef{RunID: runspec.RunID(attempt.RunID), NodeID: runspec.NodeID(attempt.NodeID)}
	change := nodeTransition{Ref: ref, Actor: actor, To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(attempt.Attempt), Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess, OutputRecord: &out.Key, ManifestRecord: &artifacts.Key}}}
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	s, tenant, request := materializationTarget(t, s, records.tenant, ref)
	return s, tenant, request
}

func TestBlobMaterializationCopiesExactRecordsWithoutAnAttempt(t *testing.T) {
	s, tenant, request := blobMaterializationFixture(t)
	got, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || got.Kind != runstate.Materialize || got.Node.Attempt != 0 || got.Node.OutputRecord == nil || got.Node.ManifestRecord == nil {
		t.Fatalf("materialization: %+v: %v", got, err)
	}
	rewriteSchemaCount(t, s.db, "blob_records", 4)
	rewriteSchemaCount(t, s.db, "node_attempts", 1)
	if _, err := s.db.Exec("DELETE FROM runs WHERE team='a' AND id='run'"); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "blob_records", 2)
	request.Source, request.Policy = nil, runstate.SourceNone
	if got, err := tenant.ReconcileNode(t.Context(), request); err != nil || got.Kind != runstate.KeepTerminal || got.Node.OutputRecord == nil {
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
		_, err = transitionNodeTx(t.Context(), tx, tenant.team, nodeTransition{Ref: request.Ref, Actor: request.Actor, Cause: causeMaterialize, Extra: transitionExtra{Materialize: request.Source}})
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
	if err != nil || row.Status != runstate.StatusPending || row.OutputRecord != nil || row.ManifestRecord != nil {
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

func TestBlobMaterializationCoalescingRequiresPersistedReadAuthority(t *testing.T) {
	_, tenant, request := blobMaterializationFixture(t)
	request.Policy, request.Source.Kind = runstate.SourceCoalesced, runstate.SourceCoalesced
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
			if err != nil || got.Kind != runstate.Dispatch || got.Node.Status != runstate.StatusPending || got.Node.StateSeq != 0 || got.Node.OutputRecord != nil || got.Claim != nil {
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

// A failed-only retry of a verified run whose job runs on a long-lived host
// writes that host's scope; a source result it cannot republish there is a
// miss that runs the job again, never a hard failure.
func TestHostScopedRetryTreatsAnUncopyableSourceAsAMiss(t *testing.T) {
	s, tenant, request := blobMaterializationFixture(t)
	request.MaterializeOnly = true
	app := runspec.AppFacts{Event: "push", RepositoryID: 1, Ref: "refs/heads/main", SHA: fixtureFeatureSHA, DeliveryKey: "main-delivery", DefaultBranch: "main", DefaultBranchPush: true, VerifiedAt: time.Unix(1, 0).UTC()}
	verified, err := runspec.AppProvenance(app, runspec.Optional[runspec.PullRequest]{}, "ci")
	if err != nil {
		t.Fatal(err)
	}
	body := sealWith(t, runspec.SealedRequest{Version: 1, Request: runspec.Request{Git: runspec.Git{Ref: app.Ref, SHA: app.SHA, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: app}}}}, verified)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET request_json=? WHERE id IN ('run','target')", body)
	host := blob.RefScope{Ref: "refs/heads/main", Writer: hostWriterPrefix + "0123456789abcdef"}
	var raw []byte
	if err := s.db.QueryRow("SELECT authority_json FROM capabilities WHERE prefix='target-owner'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record CapabilityRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.Value.Data.WriteRef, record.Value.Data.ReadRefs = host.CapabilityScope(), []string{host.CapabilityScope(), "refs/heads/main"}
	if raw, err = json.Marshal(record); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET authority_json=?,ref_scope=? WHERE prefix='target-owner'", raw, host.CapabilityScope())
	got, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || got.Kind != runstate.Dispatch || got.Node.OutputRecord != nil {
		t.Fatalf("host-scoped retry of an uncopyable result = %+v, %v; want the job to run", got, err)
	}
}

func TestScopeAcceptsCopyOnlyDownward(t *testing.T) {
	verified := blob.RefScope{Ref: "refs/heads/main", Verified: true}
	host := blob.RefScope{Ref: "refs/heads/main", Writer: hostWriterPrefix + "aa"}
	for _, tc := range []struct {
		dest, source blob.RefScope
		want         bool
	}{
		{host, host, true},
		{host, verified, true},
		{verified, host, false},
		{host, blob.RefScope{Ref: "refs/heads/main", Writer: hostWriterPrefix + "bb"}, false},
		{host, blob.RefScope{Ref: "refs/heads/other", Verified: true}, false},
		{blob.RefScope{Ref: "refs/heads/main", Writer: "account/x"}, verified, false},
	} {
		if got := scopeAcceptsCopy(tc.dest, tc.source); got != tc.want {
			t.Errorf("scopeAcceptsCopy(%+v, %+v) = %v, want %v", tc.dest, tc.source, got, tc.want)
		}
	}
}
