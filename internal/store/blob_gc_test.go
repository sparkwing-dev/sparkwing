package store

import (
	"context"
	"errors"
	"fmt"
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
	digest := strings.Repeat("a", 64)
	rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)", digest)
	marked, err := op.MarkBlobTombstones(t.Context(), 10)
	if err != nil || marked != 1 {
		t.Fatalf("mark=%d,%v", marked, err)
	}
	calls := 0
	refuse := blobDeleteFunc(func(context.Context, blob.Blob) error { calls++; return errors.New("object store unavailable") })
	if n, err := op.CollectBlobTombstones(t.Context(), 10, refuse); err != nil || n != 0 || calls != 0 {
		t.Fatalf("deleted before grace: %d,%v,%d", n, err, calls)
	}
	now = now.Add(limits.BlobGCGrace)
	if _, err := op.CollectBlobTombstones(t.Context(), 10, refuse); err == nil {
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
	if n, err := op.CollectBlobTombstones(t.Context(), 10, success); err != nil || n != 1 {
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
	if n, err := s.AsOperator().MarkBlobTombstones(t.Context(), 10); err != nil || n != 0 {
		t.Fatalf("live receipt collected: %d,%v", n, err)
	}
}

func TestBlobGCReferenceProtectsOldBytesAcrossTeams(t *testing.T) {
	for mask := 1; mask < 7; mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			s := authorityStore(t)
			rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)", DefaultTeam)
			now := time.Unix(1_800_000_000, 0).UTC()
			s.clock = func() time.Time { return now }
			op := s.AsOperator()
			root, leaf := strings.Repeat("a", 64), strings.Repeat("b", 64)
			teams := []Team{DefaultTeam, "a", "b"}
			protected := 0
			for i, team := range teams {
				for _, digest := range []string{root, leaf} {
					rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,1)", team, digest, i+1)
				}
				rewriteSchemaExec(t, s.db, "INSERT INTO blob_members(team,manifest_digest,member_digest) VALUES (?,?,?)", team, root, leaf)
				if mask&(1<<i) != 0 {
					protected++
					rewriteSchemaExec(t, s.db, `INSERT INTO blob_records(team,kind,scope,name,digest,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at) VALUES (?,'depcache','fixture','modules',?,'gh:1','manual:','runner','local',?,'job','retired',0,0,?,1)`, team, root, []byte(`{}`), []byte(`{}`))
				}
			}
			want := int64(2 * (3 - protected))
			if n, err := op.MarkBlobTombstones(t.Context(), 20); err != nil || n != want {
				t.Fatalf("tenant reachability mark=%d, want %d: %v", n, want, err)
			}
			now = now.Add(limits.BlobGCGrace)
			removed := map[string]int{}
			deleter := blobDeleteFunc(func(_ context.Context, b blob.Blob) error {
				for i, team := range teams {
					if b.Team == string(team) && (mask&(1<<i) != 0 || b.Size != int64(i+1)) {
						t.Fatalf("cross-team graph or metadata: %+v", b)
					}
				}
				removed[b.Team]++
				return nil
			})
			if n, err := op.CollectBlobTombstones(t.Context(), 20, deleter); err != nil || n != want {
				t.Fatalf("tenant collection=%d, want %d: %v", n, want, err)
			}
			for i, team := range teams {
				expected := 2
				if mask&(1<<i) != 0 {
					expected = 0
				}
				if removed[string(team)] != expected {
					t.Fatalf("team=%s removed=%d, want %d", team, removed[string(team)], expected)
				}
			}
			rewriteSchemaCount(t, s.db, "blobs", 2*protected)
		})
	}
}
