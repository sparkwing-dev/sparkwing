package store

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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

func TestBlobPersistencePublishedAttemptDataSelectors(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	addBlobJob(t, s, blobIntent(actor, attempt), "other")
	publisher := authorityRoot("publisher", "a")
	publisher.Grants = []capability.Grant{capability.DataRead, capability.DataWrite}
	publisher.Binding = capability.Binding{Kind: capability.BindingRun, RunID: attempt.RunID}
	publisher.Data = &capability.DataScope{Use: capability.DataPublish, RepositoryKey: "gh:1", RunID: attempt.RunID, ReadRefs: []string{"manual:"}, WriteRef: "manual:"}
	authorityPersist(t, s, publisher)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET intake_mode='publish' WHERE team='a' AND id=?", attempt.RunID)
	selected := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}
	intent := blob.UploadIntent{
		Authority: blob.Authority{CapabilityRef: publisher.Prefix, Attempt: selected},
		Identity:  blob.UploadIdentity{Kind: blob.UploadPublication, Team: "a", RunID: attempt.RunID, Principal: "publisher", CapabilityRef: publisher.Prefix, Attempt: selected},
		Digest:    blob.Digest(strings.Repeat("a", 64)), Size: 3,
	}
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	authorized := capability.Authorized{Principal: capability.Principal{Capability: publisher}}
	for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataRecord, capability.DataUpload, capability.DataCommit} {
		for _, selector := range []string{"own", "other_job", "foreign_run", "attempt", "generation", "missing"} {
			t.Run(string(operation)+"/"+selector, func(t *testing.T) {
				target := capability.DataTarget{Operation: operation, Digest: string(intent.Digest), RecordKind: string(blob.KindOutput), RunID: attempt.RunID, NodeID: attempt.NodeID, Attempt: attempt.Attempt, Generation: attempt.Generation}
				switch selector {
				case "other_job":
					target.NodeID = "other"
				case "foreign_run":
					target.RunID = "foreign"
				case "attempt":
					target.Attempt++
				case "generation":
					target.Generation++
				case "missing":
					target.NodeID, target.Attempt, target.Generation = "", 0, 0
				}
				err := tenant.CheckDataAccess(t.Context(), authorized, target)
				allowed := selector == "own" || selector == "other_job" && (operation == capability.DataUpload || operation == capability.DataCommit)
				if allowed && err != nil || !allowed && err == nil {
					t.Fatalf("published attempt selector %s: %v", selector, err)
				}
			})
		}
	}
}
