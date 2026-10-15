package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type blobVerifierFunc func(context.Context, blob.UploadIntent) error

func (f blobVerifierFunc) VerifyCommittedBlob(ctx context.Context, intent blob.UploadIntent) error {
	return f(ctx, intent)
}

func blobIntent(actor Actor, attempt runspec.AttemptIdentity) blob.UploadIntent {
	authority := blob.Authority{CapabilityRef: actor.CapabilityPrefix, Principal: "machine", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}}
	return blob.UploadIntent{Authority: authority, Identity: blob.UploadIdentity{Kind: blob.UploadJob, Team: attempt.Team, RunID: attempt.RunID, Principal: "machine", CapabilityRef: actor.CapabilityPrefix, Attempt: authority.Attempt}, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}
}

func TestBlobPersistenceReceiptIdentityAndDeduplication(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	verifier := blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })
	first, err := tenant.CommitBlob(t.Context(), intent, verifier)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tenant.CommitBlob(t.Context(), intent, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Identity.Attempt.Value != attempt {
		t.Fatalf("receipt drift: %+v %+v", first, second)
	}
	rewriteSchemaCount(t, s.db, "blobs", 1)
	rewriteSchemaCount(t, s.db, "blob_receipts", 1)
	intent.Size++
	if _, err := tenant.CommitBlob(t.Context(), intent, verifier); !errors.Is(err, blob.ErrIntegrity) {
		t.Fatalf("conflicting bytes: %v", err)
	}
	rewriteSchemaCount(t, s.db, "blob_receipts", 1)
}

func TestBlobPersistenceVerificationAndReauthorization(t *testing.T) {
	for _, problem := range []string{"integrity", "revoked", "new_generation", "wrong_principal", "wrong_team", "wrong_job", "claimed_ref", "missing_verifier"} {
		t.Run(problem, func(t *testing.T) {
			s, tenant, actor, attempt := resultFixture(t)
			intent := blobIntent(actor, attempt)
			var verifier BlobVerifier = blobVerifierFunc(func(context.Context, blob.UploadIntent) error {
				switch problem {
				case "integrity":
					return blob.ErrIntegrity
				case "revoked":
					rewriteSchemaExec(t, s.db, "UPDATE capabilities SET revoked_at=1 WHERE prefix='machine'")
				case "new_generation":
					rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation=8 WHERE run_id='run'")
				}
				return nil
			})
			switch problem {
			case "wrong_principal":
				intent.Identity.Principal = "other"
			case "wrong_team":
				intent.Identity.Team = "b"
			case "wrong_job":
				intent.Identity.Attempt.Value.NodeID = "other"
			case "claimed_ref":
				intent.Authority.Ref = blob.RefFacts{Scope: blob.RefScope{Ref: "main", Verified: true}}
			case "missing_verifier":
				verifier = nil
			}
			if _, err := tenant.CommitBlob(t.Context(), intent, verifier); err == nil {
				t.Fatal("unverified or foreign receipt accepted")
			}
			rewriteSchemaCount(t, s.db, "blobs", 0)
			rewriteSchemaCount(t, s.db, "blob_receipts", 0)
		})
	}
}

func TestBlobPersistenceOwnReceiptDoesNotTransferWithinRun(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	if _, err := tenant.CommitBlob(t.Context(), intent, blobVerifierFunc(func(context.Context, blob.UploadIntent) error { return nil })); err != nil {
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

func TestBlobPersistenceChecksIdentityBeforeVerification(t *testing.T) {
	_, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	intent.Identity.Principal = "foreign"
	called := false
	verifier := blobVerifierFunc(func(context.Context, blob.UploadIntent) error { called = true; return nil })
	if _, err := tenant.CommitBlob(t.Context(), intent, verifier); err == nil || called {
		t.Fatalf("unauthorized verification: called=%v, err=%v", called, err)
	}
}
