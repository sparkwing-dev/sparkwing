package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRemoteCacheKnownDigestCannotReadAnotherTeam(t *testing.T) {
	s, records, intent := blobRestoreFixture(t)
	plantBlobRestore(t, s, records, intent, blob.KindDepcache, "refs/heads/main", restoreFacts("refs/heads/main", true), false)
	reader := authorityRoot("remote-cache-b", "b")
	reader.Grants = []capability.Grant{capability.DataRead}
	reader.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"refs/heads/main"}, VerifiedDefaultOnly: true}
	authorityPersist(t, s, reader)
	foreign := &Tenant{s: s, team: "b"}
	authority, err := foreign.ResolveBlobAuthority(t.Context(), blob.Authority{CapabilityRef: reader.Prefix}, capability.DataRead)
	if err != nil {
		t.Fatal(err)
	}
	for _, duplicate := range []bool{false, true} {
		if duplicate {
			rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('b',?,3,1)", intent.Digest)
		}
		if metadata, err := foreign.ReadBlob(t.Context(), authority, intent.Digest); err == nil || metadata != (blob.Blob{}) {
			t.Fatalf("foreign cache reader got metadata: %+v: %v", metadata, err)
		}
		presence, err := foreign.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: authority, Digests: []blob.Digest{intent.Digest}, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}})
		if err != nil || len(presence.Present) != 0 {
			t.Fatalf("foreign cache reader learned presence: %+v: %v", presence, err)
		}
	}
}

func TestMemoWritersCannotReplaceVerifiedDefaultScope(t *testing.T) {
	for _, writer := range []string{"manual", "pull_request"} {
		t.Run(writer, func(t *testing.T) {
			fixture := blobRecordFixture
			if writer == "pull_request" {
				fixture = blobRestoreFixture
			}
			s, records, intent := fixture(t)
			key := plantBlobRestore(t, s, records, intent, blob.KindMemo, "refs/heads/main", restoreFacts("refs/heads/main", true), false)
			request := blobOutputRequest(intent)
			request.Key = key
			if _, err := records.CommitRecord(t.Context(), request); err == nil {
				t.Fatal("writer replaced the verified default memo")
			}
			reader := authorityRoot("remote-cache", "a")
			reader.Grants = []capability.Grant{capability.DataRead}
			reader.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"refs/heads/main"}, VerifiedDefaultOnly: true}
			authorityPersist(t, s, reader)
			hit, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: blob.Authority{CapabilityRef: reader.Prefix}, Key: key})
			if err != nil || !hit.Found || !hit.VerifiedDefault || hit.Record.Digest != intent.Digest {
				t.Fatalf("default memo changed after refused write: %+v: %v", hit, err)
			}
		})
	}
}
