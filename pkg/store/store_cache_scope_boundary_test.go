package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestStoreCacheScope(t *testing.T) {
	s, records, intent := blobRestoreFixture(t)
	// safety: an upload receipt must not hide a refused cache edge in this proof.
	rewriteSchemaExec(t, s.db, "DELETE FROM blob_receipts")
	path := blobAccountingDiskStore(t, s)
	fixture := t
	for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
		for _, test := range []struct {
			ref, defaultBranch string
			verifiedDefault    bool
			allowed            bool
		}{
			{"feature", "main", false, true},
			{"release", "main", false, kind == blob.KindDepcache},
			{"release", "release", true, kind == blob.KindDepcache},
			{"main", "main", false, false},
			{"main", "main", true, true},
		} {
			t.Run(fmt.Sprintf("%s/%s/%s/%t", kind, test.ref, test.defaultBranch, test.verifiedDefault), func(t *testing.T) {
				rewriteSchemaExec(t, s.db, "DELETE FROM blob_records")
				facts := blob.RefFacts{Scope: blob.RefScope{Ref: test.ref, Verified: true}, SHA: "fixture-sha", AppDeliveryKey: "fixture-delivery", DefaultBranch: test.defaultBranch, DefaultBranchVerified: test.verifiedDefault}
				plantBlobRestore(t, s, records, intent, kind, test.ref, facts, false)
				if err := s.db.Close(); err != nil {
					t.Fatal(err)
				}
				s.db = blobAccountingDB(fixture, path)
				presence, err := records.tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: intent.Authority, Digests: []blob.Digest{intent.Digest}})
				if err != nil || (len(presence.Present) == 1) != test.allowed {
					t.Fatalf("presence=%+v, allowed=%v: %v", presence, test.allowed, err)
				}
				a := intent.Identity.Attempt.Value
				principal := capability.Principal{Capability: capability.Capability{Prefix: intent.Authority.CapabilityRef, Team: a.Team}}
				for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataRecord} {
					target := capability.DataTarget{Operation: operation, Digest: string(intent.Digest), RunID: a.RunID, NodeID: a.NodeID, Attempt: a.Attempt, Generation: a.Generation}
					err := records.tenant.CheckDataAccess(t.Context(), capability.Authorized{Principal: principal}, target)
					if test.allowed && err != nil || !test.allowed && !errors.Is(err, blob.ErrDigestNotReadable) {
						t.Fatalf("%s allowed=%v: %v", operation, test.allowed, err)
					}
				}
			})
		}
	}
}
