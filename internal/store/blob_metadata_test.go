package store

import (
	"fmt"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestBlobMetadataRequiresCurrentTenantAuthorityProperty(t *testing.T) {
	property := func(n uint64) bool {
		for _, mutation := range []string{"team", "principal", "repo", "attempt", "upload", "digest", "revoked", "edge", "tombstone"} {
			s, records, intent := blobRecordFixture(t)
			tenant := records.tenant
			actual, err := tenant.ReadBlob(t.Context(), intent.Authority, intent.Digest)
			if err != nil || actual != (blob.Blob{Team: "a", Digest: intent.Digest, Size: intent.Size}) {
				t.Logf("read own metadata=%+v: %v", actual, err)
				return false
			}
			rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('b',?,900,1)", intent.Digest)
			switch mutation {
			case "team":
				tenant = &Tenant{s: s, team: "b"}
			case "principal":
				intent.Authority.Principal = blob.Principal(fmt.Sprintf("foreign-%x", n))
			case "repo":
				intent.Authority.Repo = runspec.RepoKey(fmt.Sprintf("local:foreign-%x", n))
			case "attempt":
				intent.Authority.Attempt.Value.Generation++
			case "upload":
				intent.Authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: intent.Identity}
				intent.Authority.Upload.Value.Principal = blob.Principal(fmt.Sprintf("foreign-%x", n))
			case "digest":
				intent.Digest = blob.Digest(fmt.Sprintf("%064x", n))
			case "revoked":
				rewriteSchemaExec(t, s.db, "UPDATE capabilities SET revoked_at=? WHERE prefix=?", s.now().UnixNano(), intent.Authority.CapabilityRef)
			case "edge":
				rewriteSchemaExec(t, s.db, "DELETE FROM blob_receipts WHERE team='a'")
			case "tombstone":
				rewriteSchemaExec(t, s.db, "INSERT INTO blob_tombstones(team,digest,marked_at) VALUES ('a',?,1)", intent.Digest)
			}
			actual, err = tenant.ReadBlob(t.Context(), intent.Authority, intent.Digest)
			if err == nil || actual != (blob.Blob{}) {
				t.Logf("%s returned metadata=%+v: %v", mutation, actual, err)
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 16})); err != nil {
		t.Fatal(err)
	}
}

func TestBlobMetadataFollowsReadEdgesWithoutCreatingThem(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	if _, err := records.CommitRecord(t.Context(), blobOutputRequest(intent)); err != nil {
		t.Fatal(err)
	}
	reader := addBlobJob(t, s, intent, "reader")
	finishBlobRecordJob(t, records, intent)
	if result, err := records.tenant.ReadBlob(t.Context(), reader.Authority, intent.Digest); err == nil || result != (blob.Blob{}) {
		t.Fatalf("unrelated reader got metadata=%+v: %v", result, err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='reader'", []byte(`{"id":"reader","deps":["job"]}`))
	if result, err := records.tenant.ReadBlob(t.Context(), reader.Authority, intent.Digest); err != nil || result.Digest != intent.Digest || result.Size != intent.Size {
		t.Fatalf("dependency reader got metadata=%+v: %v", result, err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM blob_records WHERE team='a'")
	if result, err := records.tenant.ReadBlob(t.Context(), reader.Authority, intent.Digest); err == nil || result != (blob.Blob{}) {
		t.Fatalf("removed edge got metadata=%+v: %v", result, err)
	}
	rewriteSchemaCount(t, s.db, "blob_receipts", 1)
}
