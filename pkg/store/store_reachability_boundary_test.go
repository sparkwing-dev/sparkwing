package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestStoreReachabilityRetention(t *testing.T) {
	s, records, change := blobResultFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_hash='same'")
	now := s.now()
	s.clock = func() time.Time { return now }
	intent := blobIntent(change.Actor, runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 2, Generation: 7})
	intent.Authority.Ref.Scope = fixtureRef("main")
	expires := now.Add(time.Minute)
	for _, object := range []struct {
		kind    blob.Kind
		digest  string
		size    int64
		expires time.Time
	}{
		{blob.KindDepcache, "e", 13, expires},
		{blob.KindSourceBundle, "1", 19, now.Add(200 * 24 * time.Hour)},
	} {
		intent.Digest, intent.Size = blob.Digest(strings.Repeat(object.digest, 64)), object.size
		if _, err := commitTestBlob(t, records.tenant, intent); err != nil {
			t.Fatal(err)
		}
		request := blobOutputRequest(intent)
		request.Key.Kind, request.Key.Name = object.kind, "fixture"
		if object.kind == blob.KindDepcache {
			request.Key.Scope.Pipeline, request.Key.Scope.Job = "", ""
		}
		request.ExpiresAt = runspec.Optional[time.Time]{Set: true, Value: object.expires}
		if _, err := records.CommitRecord(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	s.SetResultBlobRecords(records.manifests)
	_, tenant, request := materializationTarget(t, s, records.tenant, change.Ref)
	copied, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || copied.Kind != Materialize {
		t.Fatalf("copy: %+v: %v", copied, err)
	}
	if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: "target", Actor: request.Actor, Cause: RunFinish, To: RunSuccess}); err != nil {
		t.Fatal(err)
	}
	path := blobAccountingDiskStore(t, s)
	reopen := func() {
		t.Helper()
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		s.db = blobAccountingDB(t, path)
	}
	op := s.AsOperator()
	lease, err := op.AcquireSingletonLease(t.Context(), "maintenance", "gc-boundary", 200*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	checkStoreGCFences(t, op, lease)
	now = expires
	retained, err := op.MaintainRetention(t.Context(), RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || retained.Records != 1 || retained.PinnedOutputs != 1 {
		t.Fatalf("retention: %+v: %v", retained, err)
	}
	if n, err := op.MarkBlobTombstones(t.Context(), lease, 100); err != nil || n != 0 {
		t.Fatalf("receipts did not protect bytes: %d: %v", n, err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
	if n, err := op.MarkBlobTombstones(t.Context(), lease, 100); err != nil || n != 1 {
		t.Fatalf("expired cache marks=%d: %v", n, err)
	}
	reopen()
	checkStoreGCFences(t, op, lease)
	calls := 0
	failure := errors.New("physical deletion unavailable")
	deleter := blobDeleteFunc(func(context.Context, blob.Blob) error { calls++; return failure })
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 100, deleter); err != nil || n != 0 || calls != 0 {
		t.Fatalf("grace: removed=%d calls=%d: %v", n, calls, err)
	}
	now = now.Add(limits.BlobGCGrace)
	if _, err := op.CollectBlobTombstones(t.Context(), lease, 100, deleter); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("deletion failure: calls=%d: %v", calls, err)
	}
	rewriteSchemaCount(t, s.db, "blobs", 4)
	rewriteSchemaCount(t, s.db, "blob_tombstones", 1)
	deleter = blobDeleteFunc(func(_ context.Context, b blob.Blob) error {
		if b.Team != "a" || b.Digest != blob.Digest(strings.Repeat("e", 64)) || b.Size != 13 {
			t.Fatalf("collected reachable blob: %+v", b)
		}
		return nil
	})
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 100, deleter); err != nil || n != 1 {
		t.Fatalf("collect=%d: %v", n, err)
	}
	reopen()
	checkStoreRetainedGraphBytes(t, tenant, 25)
	var sources int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM blob_records WHERE team='a' AND run_id='target' AND source_run_id='run'").Scan(&sources); err != nil || sources != 2 {
		t.Fatalf("copied provenance: %d: %v", sources, err)
	}
	now = now.Add(120 * 24 * time.Hour)
	retained, err = op.MaintainRetention(t.Context(), RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || retained.PinnedRuns != 1 || retained.PinnedOutputs != 1 || retained.Records != 0 {
		t.Fatalf("pinned output: %+v: %v", retained, err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM pipeline_success_pins WHERE team='a'")
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='target'")
	if n, err := op.MarkBlobTombstones(t.Context(), lease, 100); err != nil || n != 2 {
		t.Fatalf("last copy marks=%d: %v", n, err)
	}
	now = now.Add(limits.BlobGCGrace)
	deleter = blobDeleteFunc(func(_ context.Context, b blob.Blob) error {
		if b.Digest != blob.Digest(strings.Repeat("a", 64)) && b.Digest != blob.Digest(strings.Repeat("b", 64)) {
			t.Fatalf("source record lost bytes: %+v", b)
		}
		return nil
	})
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 100, deleter); err != nil || n != 2 {
		t.Fatalf("last copy collection=%d: %v", n, err)
	}
	checkStoreRetainedGraphBytes(t, tenant, 19)
	now = now.Add(80 * 24 * time.Hour)
	lease, err = op.AcquireSingletonLease(t.Context(), "maintenance", "gc-boundary", 2*limits.BlobGCGrace)
	if err != nil {
		t.Fatal(err)
	}
	retained, err = op.MaintainRetention(t.Context(), RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || retained.Records != 1 {
		t.Fatalf("source expiry: %+v: %v", retained, err)
	}
	if n, err := op.MarkBlobTombstones(t.Context(), lease, 100); err != nil || n != 1 {
		t.Fatalf("source mark=%d: %v", n, err)
	}
	now = now.Add(limits.BlobGCGrace)
	deleter = blobDeleteFunc(func(_ context.Context, b blob.Blob) error {
		if b.Digest != blob.Digest(strings.Repeat("1", 64)) || b.Size != 19 {
			t.Fatalf("wrong source deletion: %+v", b)
		}
		return nil
	})
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 100, deleter); err != nil || n != 1 {
		t.Fatalf("source collection=%d: %v", n, err)
	}
	checkStoreRetainedGraphBytes(t, tenant, 0)
	now = lease.ExpiresAt
	refuseStoreGCCollection(t, op, lease)
	replacement, err := op.AcquireSingletonLease(t.Context(), "maintenance", lease.Holder, time.Hour)
	if err != nil || replacement.Generation <= lease.Generation {
		t.Fatalf("takeover: %+v: %v", replacement, err)
	}
	refuseStoreGCCollection(t, op, lease)
	if n, err := op.CollectBlobTombstones(t.Context(), replacement, 100, deleter); err != nil || n != 0 {
		t.Fatalf("empty collection=%d: %v", n, err)
	}
}

func checkStoreRetainedGraphBytes(t *testing.T, tenant *Tenant, want int64) {
	t.Helper()
	got, err := tenant.StorageRetainedBytes(t.Context())
	if err != nil || got != want {
		t.Fatalf("retained bytes=%d, want %d: %v", got, want, err)
	}
}

func TestStoreReachabilityNewReferenceWinsMarkedCollection(t *testing.T) {
	s, records, source := blobResultFixture(t)
	_, tenant, target := materializationTarget(t, s, records.tenant, source.Ref)
	now := s.now()
	s.clock = func() time.Time { return now }
	op := s.AsOperator()
	lease, err := op.AcquireSingletonLease(t.Context(), "maintenance", "late-reference", 3*limits.BlobGCGrace)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
	if n, err := op.MarkBlobTombstones(t.Context(), lease, 100); err != nil || n != 2 {
		t.Fatalf("retired source marks=%d: %v", n, err)
	}
	identity := blob.UploadIdentity{
		Kind: blob.UploadOwnerUnit, Team: "a", RunID: "target", Principal: "host", CapabilityRef: target.Actor.CapabilityPrefix,
		Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadTriggerUnit, ID: "target", Generation: target.Actor.Owner.Generation}},
	}
	authority := blob.Authority{CapabilityRef: identity.CapabilityRef, Upload: runspec.Optional[blob.UploadIdentity]{Set: true, Value: identity}}
	intent := blob.UploadIntent{Authority: authority, Identity: identity, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}
	storeBoundaryUpload(t, tenant, intent, "rescued")
	key := blob.RecordKey{Team: "a", Kind: blob.KindSourceBundle, Scope: blob.Scope{Repo: "gh:1", Ref: fixtureRef("main"), Pipeline: "ci", Key: "rescued"}, Name: "modules"}
	if _, err := tenant.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: authority, Key: key, Source: identity, Digest: intent.Digest}); err != nil {
		t.Fatal(err)
	}
	blobAccountingDiskStore(t, s)
	now = now.Add(limits.BlobGCGrace)
	deleter := blobDeleteFunc(func(_ context.Context, b blob.Blob) error {
		if b.Digest != blob.Digest(strings.Repeat("b", 64)) {
			t.Fatalf("new reference lost marked bytes: %+v", b)
		}
		return nil
	})
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 100, deleter); err != nil || n != 1 {
		t.Fatalf("rechecked collection=%d: %v", n, err)
	}
	rewriteSchemaCount(t, s.db, "blobs", 1)
	rewriteSchemaCount(t, s.db, "blob_tombstones", 0)
}
