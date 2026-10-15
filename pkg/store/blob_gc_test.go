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
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
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

func TestBlobGCReferenceProtectsOldBytesAcrossTeams(t *testing.T) {
	s := authorityStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)", DefaultTeam)
	now := time.Unix(1_800_000_000, 0).UTC()
	s.clock = func() time.Time { return now }
	op := s.AsOperator()
	lease, err := op.AcquireSingletonLease(t.Context(), "maintenance", "gc", 3*limits.BlobGCGrace)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	for _, team := range []Team{DefaultTeam, "a", "b"} {
		old := now.Add(-120 * 24 * time.Hour).UnixNano()
		rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,3,?)", team, digest, old)
		rewriteSchemaExec(t, s.db, `INSERT INTO blob_records(team,kind,scope,name,digest,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at) VALUES (?,'depcache','fixture','modules',?,'gh:1','manual:','runner','local',?,'job','retired',0,0,?,?)`, team, digest, []byte(`{}`), []byte(`{}`), old)
	}
	if n, err := op.MarkBlobTombstones(t.Context(), lease, 10); err != nil || n != 0 {
		t.Fatalf("age overrode live records: marked=%d: %v", n, err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM blob_records")
	if n, err := op.MarkBlobTombstones(t.Context(), lease, 10); err != nil || n != 3 {
		t.Fatalf("unreferenced tenant blobs: marked=%d: %v", n, err)
	}
	now = now.Add(limits.BlobGCGrace)
	removed := map[string]bool{}
	deleter := blobDeleteFunc(func(_ context.Context, b blob.Blob) error {
		if b.Digest != blob.Digest(digest) || b.Size != 3 || removed[b.Team] {
			t.Fatalf("unexpected collection: %+v", b)
		}
		removed[b.Team] = true
		return nil
	})
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 10, deleter); err != nil || n != 3 {
		t.Fatalf("cross-team collection=%d: %v", n, err)
	}
	for _, team := range []Team{DefaultTeam, "a", "b"} {
		if !removed[string(team)] {
			t.Errorf("collector omitted team %s", team)
		}
	}
	rewriteSchemaCount(t, s.db, "blobs", 0)
}
