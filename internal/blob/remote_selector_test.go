package blob_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func FuzzRemoteCacheSelectsServerDefaultRef(f *testing.F) {
	for _, ref := range []string{"", "main", "feature/x", "manual:main", "../main", "main\x00other"} {
		f.Add(ref, false, true)
		f.Add(ref, true, true)
		f.Add(ref, true, false)
	}
	f.Fuzz(func(t *testing.T, ref string, verified, attested bool) {
		authority := blob.Authority{CapabilityRef: "user", Principal: blob.Principal("reader:" + ref), Repo: "gh:123"}
		d := digest("cached")
		store := blob.NewSignedStore(blob.SignedStoreOptions{
			Signer: &signer{download: func(q blob.GetRequest) (blob.SignedTransfer, error) {
				if !attested {
					t.Fatal("unverified selection reached object signing")
				}
				if q.Authority != authority {
					t.Fatalf("download lost bound authority: %+v", q.Authority)
				}
				return blob.SignedTransfer{Digest: d, Size: 6}, nil
			}},
			HTTPClient: transfer(func(context.Context, blob.SignedTransfer, io.Reader) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("cached")), nil
			}),
		})
		lookups := 0
		r := records{lookup: func(_ context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
			lookups++
			if !q.VerifiedDefaultOnly || q.Key.Team != "team" || q.Key.Scope.Ref != (blob.RefScope{}) || q.Authority != authority {
				t.Fatalf("client selected remote scope: %+v", q)
			}
			key := q.Key
			key.Team, key.Scope.Ref = "team", blob.RefScope{Ref: "main", Verified: true}
			return blob.RecordLookupResult{Found: true, VerifiedDefault: attested, Record: blob.Record{Key: key, Digest: d}}, nil
		}}
		cache, err := blob.OpenRemoteCache(blob.RemoteCacheOptions{Profile: "ci", Team: "team", Authority: authority, Repo: "gh:123", Records: r}, store)
		if err != nil {
			t.Fatal(err)
		}
		budget := blob.Budget{Deadline: time.Now().Add(time.Minute)}
		localRef := blob.RefScope{Ref: ref, Verified: verified}
		hit, err := cache.Memo(t.Context(), blob.MemoKey{Repo: "gh:123", Ref: localRef, Pipeline: "build", Job: "job", Hash: runspec.Hash(digest("key"))}, budget)
		if !attested {
			if !errors.Is(err, blob.ErrDigestNotReadable) || hit.Found {
				t.Fatalf("unverified memo selection: %+v %v", hit, err)
			}
			return
		}
		if err != nil || !hit.Found {
			t.Fatalf("default memo lookup: %+v, %v", hit, err)
		}
		record, err := cache.Depcache(t.Context(), blob.DepcacheKey{Repo: "gh:123", Ref: localRef, Name: "modules", Key: "lock"}, budget)
		if err != nil || record.Digest != d || record.Key.Scope.Ref.Ref != "main" || lookups != 2 {
			t.Fatalf("default depcache lookup: %+v, %v, lookups=%d", record, err, lookups)
		}
		reader, err := cache.Artifact(t.Context(), d)
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(reader)
		if closeErr := reader.Close(); readErr != nil || closeErr != nil || !reader.Verified() || string(raw) != "cached" {
			t.Fatalf("bound artifact: %q: %v: %v", raw, readErr, closeErr)
		}
	})
}
