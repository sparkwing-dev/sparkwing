package store

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestStorePresenceReadSet(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	first := blobIntent(actor, attempt)
	second := addBlobJob(t, s, first, "b")
	unrelated := addBlobJob(t, s, first, "unrelated")
	unrelated.Digest, unrelated.Size = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", 9
	if _, err := commitTestBlob(t, tenant, unrelated); err != nil {
		t.Fatal(err)
	}
	foreign, object := storeBoundaryForeignUploader(t, s)
	if _, err := commitTestBlob(t, foreign, object); err != nil {
		t.Fatal(err)
	}
	receiptA, err := commitTestBlob(t, tenant, first)
	if err != nil {
		t.Fatal(err)
	}
	query := []blob.Digest{first.Digest, unrelated.Digest, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", object.Digest, first.Digest}
	checkStorePresenceAccess(t, s, tenant, first.Authority, query, []blob.Digest{first.Digest})
	checkStorePresenceAccess(t, s, tenant, second.Authority, query, nil)
	ticket, err := tenant.ReserveBlobUpload(t.Context(), UploadReservationRequest{ID: "independent", Intent: second, ExpiresAt: s.now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	checkStorePresenceAccess(t, s, tenant, second.Authority, query, nil)
	commit := verifiedUpload(ticket, second.Authority)
	bad := commit
	bad.Verified.Size++
	if _, err := tenant.CommitBlobUpload(t.Context(), bad); err == nil {
		t.Fatal("substituted verification minted a receipt")
	}
	checkStorePresenceAccess(t, s, tenant, second.Authority, query, nil)
	for range 2 {
		got, err := tenant.CommitBlobUpload(t.Context(), commit)
		if err != nil || got.Identity != second.Identity || got.Identity == receiptA.Identity {
			t.Fatalf("independent receipt: %+v: %v", got, err)
		}
	}
	blobAccountingDiskStore(t, s)
	checkStorePresenceAccess(t, s, tenant, second.Authority, query, []blob.Digest{first.Digest})
	checkStorePresenceAccess(t, s, tenant, unrelated.Authority, query, []blob.Digest{unrelated.Digest})
	if _, err := tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: first.Authority, Digests: query, Budget: blob.Budget{Deadline: s.now()}}); !errors.Is(err, blob.ErrBudget) {
		t.Fatalf("expired budget: %v", err)
	}
	usage, err := tenant.StorageUsageFor(t.Context(), "run", StorageMonth(s.now()))
	if err != nil || usage.RunBytes != 12 || usage.MonthBytes != 12 || usage.RunObjects != 2 || usage.MonthObjects != 2 {
		t.Fatalf("deduplicated usage: %+v: %v", usage, err)
	}
	for _, table := range []string{"team_download_day", "egress_usage"} {
		rewriteSchemaCount(t, s.db, table, 0)
	}
}

func checkStorePresenceAccess(t *testing.T, s *Store, tenant *Tenant, authority blob.Authority, digests, present []blob.Digest) {
	t.Helper()
	result, err := tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: authority, Digests: digests})
	if err != nil || !slices.Equal(result.Present, present) {
		t.Fatalf("presence: %+v, want %v: %v", result, present, err)
	}
	a := authority.Attempt.Value
	principal := capability.Principal{Capability: capability.Capability{Prefix: authority.CapabilityRef, Team: a.Team}, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: authority.CapabilityRef}}
	for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataHasMany, capability.DataLookup} {
		for _, digest := range digests {
			target := capability.DataTarget{Operation: operation, Digest: string(digest), RunID: a.RunID, NodeID: a.NodeID, Attempt: a.Attempt, Generation: a.Generation}
			if err := checkStorePresenceVerdict(tenant.CheckDataAccess(t.Context(), capability.Authorized{Principal: principal}, target), slices.Contains(present, digest)); err != nil {
				t.Fatal(err)
			}
			for _, rule := range []capability.BindingRule{capability.BindData, capability.BindReadSet} {
				selected := capability.Target{Team: a.Team, RunID: a.RunID, NodeID: a.NodeID, Data: &target}
				if err := checkStorePresenceVerdict(s.AsOperator().CheckBinding(t.Context(), principal, rule, selected), slices.Contains(present, digest)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func checkStorePresenceVerdict(err error, readable bool) error {
	if readable && err != nil || !readable && !errors.Is(err, blob.ErrDigestNotReadable) {
		return fmt.Errorf("readable=%v: %v", readable, err)
	}
	return nil
}

func storeBoundaryUpload(t *testing.T, tenant *Tenant, intent blob.UploadIntent, id string) {
	t.Helper()
	ticket, err := tenant.ReserveBlobUpload(t.Context(), UploadReservationRequest{ID: id, Intent: intent, ExpiresAt: tenant.s.now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, intent.Authority)); err != nil || got.Identity != intent.Identity {
		t.Fatalf("receipt: %+v: %v", got, err)
	}
}
