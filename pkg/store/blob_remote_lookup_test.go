package store

import (
	"fmt"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestBlobRecordsRemoteCacheResolvesVerifiedDefault(t *testing.T) {
	for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
		t.Run(string(kind), func(t *testing.T) {
			s, records, intent := blobRestoreFixture(t)
			reader := authorityRoot("remote-cache", "a")
			reader.Grants = []capability.Grant{capability.DataRead}
			reader.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"refs/heads/main"}, VerifiedDefaultOnly: true}
			authorityPersist(t, s, reader)
			key := plantBlobRestore(t, s, records, intent, kind, "refs/heads/main", restoreFacts("refs/heads/main", true), false)
			request := blob.RecordLookup{Authority: blob.Authority{CapabilityRef: reader.Prefix}, Key: key, VerifiedDefaultOnly: true}
			request.Key.Scope.Ref = blob.RefScope{}
			hit, err := records.LookupRecord(t.Context(), request)
			if err != nil || !hit.Found || !hit.VerifiedDefault || hit.Record.Key != key || hit.Record.Digest != intent.Digest {
				t.Fatalf("server-selected default: %+v %v", hit, err)
			}
			property := func(value string, field uint8) bool {
				facts := restoreFacts("refs/heads/main", true)
				switch field % 6 {
				case 0:
					facts.DefaultBranchVerified = false
				case 1:
					facts.Scope.Verified = false
					facts.DefaultBranchVerified = false
				case 2:
					facts.AppDeliveryKey = ""
				case 3:
					facts.SHA = ""
				case 4:
					facts.DefaultBranch = "foreign:" + value
				case 5:
					facts.Scope.Ref = "foreign:" + value
				}
				plantBlobRestore(t, s, records, intent, kind, "refs/heads/main", facts, false)
				hit, _ := records.LookupRecord(t.Context(), request)
				return !hit.Found && !hit.VerifiedDefault
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBlobRemoteCacheStableFallbackCompletesProperty(t *testing.T) {
	for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
		for count := 1; count <= 9; count++ {
			t.Run(fmt.Sprintf("%s/%d", kind, count), func(t *testing.T) {
				s, records, intent := blobRestoreFixture(t)
				reader := authorityRoot("remote-cache", "a")
				reader.Grants = []capability.Grant{capability.DataRead}
				reader.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", VerifiedDefaultOnly: true}
				var last blob.Digest
				for i := range count {
					ref := fmt.Sprintf("default_%d", i)
					reader.Data.ReadRefs = append(reader.Data.ReadRefs, ref)
					facts := restoreFacts(ref, true)
					facts.DefaultBranch = ref
					object := intent
					object.Digest = blob.Digest(fmt.Sprintf("%064x", i+1))
					last = object.Digest
					rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)", object.Digest)
					plantBlobRestore(t, s, records, object, kind, ref, facts, true)
				}
				authorityPersist(t, s, reader)
				manifests := fakeBlobManifests{}
				records.manifests = manifests
				for _, found := range []bool{false, true} {
					if found {
						manifests[last] = nil
					}
					var got bool
					var err error
					if kind == blob.KindMemo {
						hit, lookupErr := records.LookupMemo(t.Context(), blob.MemoLookup{Authority: blob.Authority{CapabilityRef: reader.Prefix}, Key: blob.MemoKey{Repo: "gh:1", Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}})
						got, err = hit.Found, lookupErr
					} else {
						hit, lookupErr := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: blob.Authority{CapabilityRef: reader.Prefix}, Key: blob.DepcacheKey{Repo: "gh:1", Name: "modules", Key: fixtureMemoHash}})
						got, err = hit.Found, lookupErr
					}
					if err != nil || got != found {
						t.Fatalf("found=%v, want %v: %v", got, found, err)
					}
				}
			})
		}
	}
}
