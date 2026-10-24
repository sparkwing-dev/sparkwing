package store

import (
	"context"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestBlobCopiedOutputSurvivesSourceCollection(t *testing.T) {
	property := func(order uint8) bool {
		s, records, intent := blobRecordFixture(t)
		now := s.now()
		s.clock = func() time.Time { return now }
		memo := blobOutputRequest(intent)
		memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
		if _, err := records.CommitRecord(t.Context(), memo); err != nil {
			t.Fatal(err)
		}
		activateMemoFixture(t, s)
		lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "copy-gc", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, "INSERT INTO blob_tombstones(team,digest,marked_at) VALUES ('a',?,?)", intent.Digest, now.Add(-limits.BlobGCGrace).UnixNano())
		var hit blob.MemoHit
		var copyErr, retireErr error
		var deleted int64
		copyOutput := func() {
			hit, copyErr = records.CopyMemo(t.Context(), blob.CopyMemoRequest{
				Lookup:      blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: assertedRef("main"), Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}},
				Destination: intent.Identity.Attempt.Value,
			})
		}
		retireSource := func() {
			retireErr = s.inTx(t.Context(), func(tx *storeTx) error {
				if _, err := tx.execContext(t.Context(), "DELETE FROM blob_records WHERE team='a' AND kind='memo'"); err != nil {
					return err
				}
				_, err := tx.execContext(t.Context(), "DELETE FROM blob_receipts WHERE team='a'")
				return err
			})
			if retireErr == nil {
				deleted, retireErr = s.AsOperator().CollectBlobTombstones(t.Context(), lease, 10, blobDeleteFunc(func(_ context.Context, b blob.Blob) error {
					if b.Team != "a" || b.Digest != intent.Digest || b.Size != intent.Size {
						t.Errorf("wrong physical deletion: %+v", b)
					}
					return nil
				}))
			}
		}
		switch order % 3 {
		case 0:
			copyOutput()
			retireSource()
		case 1:
			retireSource()
			copyOutput()
		case 2:
			start, done := make(chan struct{}), make(chan struct{})
			go func() { <-start; defer close(done); copyOutput() }()
			close(start)
			retireSource()
			<-done
		}
		if copyErr != nil || retireErr != nil {
			t.Logf("copy=%v retirement=%v", copyErr, retireErr)
			return false
		}
		var blobs, outputs int64
		if err := s.db.QueryRow("SELECT COUNT(*) FROM blobs WHERE team='a'").Scan(&blobs); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("SELECT COUNT(*) FROM blob_records WHERE team='a' AND kind='output'").Scan(&outputs); err != nil {
			t.Fatal(err)
		}
		var chargedBytes, chargedObjects int64
		if err := s.db.QueryRow("SELECT COALESCE(SUM(bytes),0),COALESCE(SUM(objects),0) FROM storage_month_usage WHERE team='a'").Scan(&chargedBytes, &chargedObjects); err != nil {
			t.Fatal(err)
		}
		if chargedBytes != intent.Size || chargedObjects != 1 {
			t.Logf("copy changed upload usage: bytes=%d objects=%d", chargedBytes, chargedObjects)
			return false
		}
		retained, err := records.tenant.StorageRetainedBytes(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if hit.Found {
			return outputs == 1 && blobs == 1 && deleted == 0 && retained == intent.Size
		}
		return outputs == 0 && blobs == 0 && deleted == 1 && len(hit.ReadSet.Edges) == 0 && retained == 0
	}
	for _, order := range []uint8{0, 1, 2} {
		if !property(order) {
			t.Fatalf("copy/collection order %d violated reachability", order)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48, Rand: rand.New(rand.NewSource(625021))}); err != nil {
		t.Fatal(err)
	}
}
