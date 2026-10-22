package store

import (
	"errors"
	"strconv"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func FuzzOutputRecordUsesSharedLimit(f *testing.F) {
	for _, size := range []uint64{0, 1, uint64(limits.NodeOutputBytes - 1), uint64(limits.NodeOutputBytes), uint64(limits.NodeOutputBytes + 1), 64 << 20} {
		f.Add(size)
	}
	f.Fuzz(func(t *testing.T, value uint64) {
		s, records, intent := blobRecordFixture(t)
		size := int64(value % (2*uint64(limits.NodeOutputBytes) + 1))
		rewriteSchemaExec(t, s.db, "UPDATE blobs SET size=? WHERE team='a' AND digest=?", size, intent.Digest)
		_, err := records.tenant.CommitRecord(t.Context(), blobOutputRequest(intent))
		if size <= limits.NodeOutputBytes {
			if err != nil {
				t.Fatalf("size %d: %v", size, err)
			}
			return
		}
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.PayloadTooLarge {
			t.Fatalf("oversized output %d: %v", size, err)
		}
		rewriteSchemaCount(t, s.db, "blob_records", 0)
	})
}

func TestBlobRecordsMemoCannotBypassOutputLimit(t *testing.T) {
	for _, size := range []int64{limits.NodeOutputBytes, limits.NodeOutputBytes + 1} {
		t.Run(strconv.FormatInt(size, 10), func(t *testing.T) {
			s, records, intent := blobRecordFixture(t)
			rewriteSchemaExec(t, s.db, "UPDATE blobs SET size=? WHERE team='a' AND digest=?", size, intent.Digest)
			memo := blobOutputRequest(intent)
			memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
			_, err := records.CommitRecord(t.Context(), memo)
			if size > limits.NodeOutputBytes {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.PayloadTooLarge {
					t.Fatalf("oversized output refusal=%v, want payload_too_large", err)
				}
				rewriteSchemaCount(t, s.db, "blob_records", 0)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			activateMemoFixture(t, s)
			request := blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: assertedRef("main"), Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}, Destination: intent.Identity.Attempt.Value}
			if hit, err := records.CopyMemo(t.Context(), request); err != nil || !hit.Found || hit.Output.Value != intent.Digest {
				t.Fatalf("boundary-sized output copy=%+v: %v", hit, err)
			}
		})
	}
}

func TestBlobRecordsOversizedMemoIsAMissWithoutPartialEdges(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	memo := blobOutputRequest(intent)
	memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
	if _, err := records.CommitRecord(t.Context(), memo); err != nil {
		t.Fatal(err)
	}
	activateMemoFixture(t, s)
	// safety: persisted cache corruption must not manufacture an oversized output edge.
	rewriteSchemaExec(t, s.db, "UPDATE blobs SET size=? WHERE team='a' AND digest=?", limits.NodeOutputBytes+1, intent.Digest)
	lookup := blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: assertedRef("main"), Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}
	if hit, err := records.LookupMemo(t.Context(), lookup); err != nil || hit.Found {
		t.Fatalf("oversized memo lookup=%+v: %v", hit, err)
	}
	if hit, err := records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: lookup, Destination: intent.Identity.Attempt.Value}); err != nil || hit.Found {
		t.Fatalf("oversized memo copy=%+v: %v", hit, err)
	}
	rewriteSchemaCount(t, s.db, "blob_records", 1)
	if _, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: memo.Key}); !errors.Is(err, blob.ErrIntegrity) {
		t.Fatalf("required lookup=%v, want integrity failure", err)
	}
}

func TestBlobRecordsMemoOutputLimitDoesNotCapManifests(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE blobs SET size=? WHERE team='a' AND digest=?", limits.NodeOutputBytes+1, intent.Digest)
	records.manifests = fakeBlobManifests{intent.Digest: nil}
	memo := blobOutputRequest(intent)
	memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "manifest", fixtureMemoHash
	memo.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: intent.Digest}
	if _, err := records.CommitRecord(t.Context(), memo); err != nil {
		t.Fatal(err)
	}
	activateMemoFixture(t, s)
}
