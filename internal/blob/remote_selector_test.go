package blob_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func FuzzRemoteCacheSelectsServerDefaultRef(f *testing.F) {
	for _, ref := range []string{"", "main", "feature/x", "manual:main", "../main", "main\x00other"} {
		f.Add(ref, false)
		f.Add(ref, true)
	}
	f.Fuzz(func(t *testing.T, ref string, verified bool) {
		d := digest("cached")
		store := blob.NewSignedStore(blob.SignedStoreOptions{
			Signer: &signer{transfer: blob.SignedTransfer{Digest: d, Size: 6}},
			HTTPClient: transfer(func(context.Context, blob.SignedTransfer, io.Reader) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("cached")), nil
			}),
		})
		lookups := 0
		r := records{lookup: func(_ context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
			lookups++
			if !q.VerifiedDefaultOnly || q.Key.Team != "" || q.Key.Scope.Ref != (blob.RefScope{}) || q.Authority.Repo != "gh:123" {
				t.Fatalf("client selected remote scope: %+v", q)
			}
			key := q.Key
			key.Team, key.Scope.Ref = "team", blob.RefScope{Ref: "main", Verified: true}
			return blob.RecordLookupResult{Found: true, Record: blob.Record{Key: key, Digest: d, RefFacts: blob.RefFacts{
				Scope: key.Scope.Ref, DefaultBranch: "main", DefaultBranchVerified: true, AppDeliveryKey: "delivery", SHA: "sha",
			}}}, nil
		}}
		cache, err := blob.OpenRemoteCache(blob.RemoteCacheOptions{Profile: "ci", AuthorityRef: "user", Repo: "gh:123", Records: r}, store)
		if err != nil {
			t.Fatal(err)
		}
		budget := blob.Budget{Deadline: time.Now().Add(time.Minute)}
		localRef := blob.RefScope{Ref: ref, Verified: verified}
		hit, err := cache.Memo(t.Context(), blob.MemoKey{Repo: "gh:123", Ref: localRef, Pipeline: "build", Job: "job", Hash: runspec.Hash(digest("key"))}, budget)
		if err != nil || !hit.Found {
			t.Fatalf("default memo lookup: %+v, %v", hit, err)
		}
		record, err := cache.Depcache(t.Context(), blob.DepcacheKey{Repo: "gh:123", Ref: localRef, Name: "modules", Key: "lock"}, budget)
		if err != nil || record.Digest != d || record.Key.Scope.Ref.Ref != "main" || lookups != 2 {
			t.Fatalf("default depcache lookup: %+v, %v, lookups=%d", record, err, lookups)
		}
	})
}
