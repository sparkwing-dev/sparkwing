package store

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func blobIntent(actor Actor, attempt runspec.AttemptIdentity) blob.UploadIntent {
	authority := blob.Authority{CapabilityRef: actor.CapabilityPrefix, Principal: "machine", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}}
	return blob.UploadIntent{Authority: authority, Identity: blob.UploadIdentity{Kind: blob.UploadJob, Team: attempt.Team, RunID: attempt.RunID, Principal: "machine", CapabilityRef: actor.CapabilityPrefix, Attempt: authority.Attempt}, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}
}

func TestBlobPersistenceOwnReceiptDoesNotTransferWithinRun(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*blob.UploadIdentity){func(i *blob.UploadIdentity) { i.Attempt.Value.NodeID = "other" }, func(i *blob.UploadIdentity) { i.Attempt.Value.Attempt++ }, func(i *blob.UploadIdentity) { i.Attempt.Value.Generation++ }, func(i *blob.UploadIdentity) { i.CapabilityRef = "other" }} {
		source := intent.Identity
		change(&source)
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			receipt, err := readOwnBlobReceiptTx(t.Context(), tx, source, intent.Digest)
			if receipt.Set {
				t.Fatal("receipt transferred")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}
