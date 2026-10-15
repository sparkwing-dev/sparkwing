package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type blobDeleteFunc func(context.Context, blob.Blob) error

func (f blobDeleteFunc) DeleteBlob(ctx context.Context, b blob.Blob) error { return f(ctx, b) }

func TestBlobGCGraceReachabilityAndDeletionFailure(t *testing.T) {
	s := authorityStore(t)
	now := time.Unix(1000, 0).UTC()
	s.clock = func() time.Time { return now }
	op := s.AsOperator()
	lease, err := op.AcquireSingletonLease(t.Context(), "maintenance", "gc", 3*limits.BlobGCGrace)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)", digest)
	marked, err := op.MarkBlobTombstones(t.Context(), lease, 10)
	if err != nil || marked != 1 {
		t.Fatalf("mark=%d,%v", marked, err)
	}
	calls := 0
	refuse := blobDeleteFunc(func(context.Context, blob.Blob) error { calls++; return errors.New("object store unavailable") })
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 10, refuse); err != nil || n != 0 || calls != 0 {
		t.Fatalf("deleted before grace: %d,%v,%d", n, err, calls)
	}
	now = now.Add(limits.BlobGCGrace)
	if _, err := op.CollectBlobTombstones(t.Context(), lease, 10, refuse); err == nil {
		t.Fatal("object deletion failure ignored")
	}
	rewriteSchemaCount(t, s.db, "blobs", 1)
	rewriteSchemaCount(t, s.db, "blob_tombstones", 1)
	success := blobDeleteFunc(func(_ context.Context, b blob.Blob) error {
		if b.Team != "a" || b.Size != 3 {
			t.Fatalf("wrong blob: %+v", b)
		}
		return nil
	})
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 10, success); err != nil || n != 1 {
		t.Fatalf("collect=%d,%v", n, err)
	}
	rewriteSchemaCount(t, s.db, "blobs", 0)
	rewriteSchemaCount(t, s.db, "blob_tombstones", 0)
}

func TestBlobGCReceiptProtectsUploadedBytesAndClearsTombstone(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)", intent.Digest)
	rewriteSchemaExec(t, s.db, "INSERT INTO blob_tombstones(team,digest,marked_at) VALUES ('a',?,1)", intent.Digest)
	if _, err := tenant.CommitBlob(t.Context(), intent, blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "blob_tombstones", 0)
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "gc", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.AsOperator().MarkBlobTombstones(t.Context(), lease, 10); err != nil || n != 0 {
		t.Fatalf("live receipt collected: %d,%v", n, err)
	}
}
