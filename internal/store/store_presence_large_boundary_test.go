package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestStoreLargePresenceBudget(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	own := blobIntent(actor, attempt)
	unrelated := addBlobJob(t, s, own, "unrelated")
	foreign, otherTeam := storeBoundaryForeignUploader(t, s)
	var digests, want []blob.Digest
	for i := range 1025 {
		sum := sha256.Sum256(fmt.Appendf(nil, "presence-%d", i))
		digest := blob.Digest(hex.EncodeToString(sum[:]))
		digests = append(digests, digest)
		switch i % 4 {
		case 0:
			upload := own
			upload.Digest, upload.Size = digest, 1
			storeBoundaryUpload(t, tenant, upload, fmt.Sprintf("own_%d", i))
			want = append(want, digest)
		case 1:
			upload := unrelated
			upload.Digest, upload.Size = digest, 1
			storeBoundaryUpload(t, tenant, upload, fmt.Sprintf("unrelated_%d", i))
		case 2:
			upload := otherTeam
			upload.Digest, upload.Size = digest, 1
			storeBoundaryUpload(t, foreign, upload, fmt.Sprintf("foreign_%d", i))
		}
	}
	blobAccountingDiskStore(t, s)
	for range 2 {
		query := blob.HasManyRequest{Authority: own.Authority, Digests: append(slices.Clone(digests), digests...), Budget: blob.Budget{Deadline: s.now().Add(time.Minute)}}
		result, err := tenant.ReadableBlobs(t.Context(), query)
		if err != nil || !slices.Equal(result.Present, want) {
			t.Fatalf("large presence got %d digests, want %d: %v", len(result.Present), len(want), err)
		}
		query.Budget.Deadline = s.now()
		if result, err := tenant.ReadableBlobs(t.Context(), query); !errors.Is(err, blob.ErrBudget) || len(result.Present) != 0 {
			t.Fatalf("expired presence: %+v: %v", result, err)
		}
		slices.Reverse(digests)
		slices.Reverse(want)
	}
	usage, err := tenant.StorageUsageFor(t.Context(), "run", StorageMonth(s.now()))
	if err != nil || usage.RunBytes != 513 || usage.MonthBytes != 513 || usage.RunObjects != 513 || usage.MonthObjects != 513 {
		t.Fatalf("large presence upload accounting: %+v: %v", usage, err)
	}
	for _, table := range []string{"team_download_day", "egress_usage"} {
		rewriteSchemaCount(t, s.db, table, 0)
	}
}
