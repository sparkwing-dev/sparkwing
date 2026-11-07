package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type preparedUploads struct {
	s      *Store
	tenant *Tenant
	commit func(id, digest string, size int64) error
}

func preparedUploadFixture(t *testing.T) preparedUploads {
	t.Helper()
	s, _, claim, _ := launcherSnapshotFixture(t, capability.UnitPlan)
	issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	if err := tenant.SetStorageQuota(t.Context(), StorageQuota{Tier: storageTierFree}); err != nil {
		t.Fatal(err)
	}
	identity := blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Team: "a", RunID: "run", Principal: "launcher", CapabilityRef: issued.Capability.Prefix, Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadPlanUnit, ID: "run", Generation: int64(claim.Award.Generation)}}}
	authority := blob.Authority{CapabilityRef: identity.CapabilityRef}
	setBlobRecordRef(t, s, identity.CapabilityRef)
	commit := func(id, digest string, size int64) error {
		ticket, err := tenant.ReserveBlobUpload(t.Context(), UploadReservationRequest{ID: id, Intent: blob.UploadIntent{Authority: authority, Identity: identity, Digest: blob.Digest(strings.Repeat(digest, 64)), Size: size}, ExpiresAt: s.now().Add(time.Hour)})
		if err != nil {
			return err
		}
		_, err = tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, authority))
		return err
	}
	return preparedUploads{s: s, tenant: tenant, commit: commit}
}

func (p preparedUploads) usage(t *testing.T) StorageUsage {
	t.Helper()
	usage, err := p.tenant.StorageUsageFor(t.Context(), "run", StorageMonth(p.s.now()))
	if err != nil {
		t.Fatal(err)
	}
	return usage
}

// A free-tier team's first prepared pipeline in a run, larger than the old 10 MiB limit, spends no quota and a
// repeated digest stores no second copy; a further upload from the same planning token is charged in full.
func TestPreparedPipelineExemptsOnlyTheRunsFirstUpload(t *testing.T) {
	p := preparedUploadFixture(t)
	const size = 30 << 20
	for _, id := range []string{"prepared", "again"} {
		if err := p.commit(id, "a", size); err != nil {
			t.Fatalf("%s: free tier refused a 30 MB prepared pipeline: %v", id, err)
		}
	}
	rewriteSchemaCount(t, p.s.db, "blobs", 1)
	if usage := p.usage(t); usage.RunBytes != 0 || usage.RunObjects != 0 || usage.MonthBytes != 0 || usage.MonthObjects != 0 {
		t.Fatalf("first prepared pipeline spent the storage quota: %+v", usage)
	}
	if err := p.commit("second", "b", size); err != nil {
		t.Fatal(err)
	}
	if usage := p.usage(t); usage.RunBytes != size || usage.RunObjects != 1 || usage.MonthBytes != size {
		t.Fatalf("second upload from the planning token was not charged: %+v", usage)
	}
}

// Bytes of a first prepared pipeline past the exempt 256 MiB count against the quota and meet its usual refusal.
func TestPreparedPipelineChargesBytesPastTheExemption(t *testing.T) {
	p := preparedUploadFixture(t)
	var refusal *StorageQuotaError
	if err := p.commit("too_big", "a", limits.PreparedPipelineBytes+freeTierQuota.MaxBytesPerRun+1); !errors.As(err, &refusal) || refusal.Limit != storageLimitBytesPerRun || refusal.Tier != storageTierFree || refusal.Requested != freeTierQuota.MaxBytesPerRun+1 {
		t.Fatalf("excess past the exemption = %v; want a free-tier bytes_per_run refusal", err)
	}
	if err := p.commit("fits", "b", limits.PreparedPipelineBytes+freeTierQuota.MaxBytesPerRun); err != nil {
		t.Fatalf("a refused upload consumed the run's exemption: %v", err)
	}
	if usage := p.usage(t); usage.RunBytes != freeTierQuota.MaxBytesPerRun || usage.RunObjects != 0 {
		t.Fatalf("usage = %+v; want only the bytes past the exemption", usage)
	}
}

// User data still meets the free tier's per-run limit, now a hundred mebibytes.
func TestFreeTierRefusesUserDataPastOneHundredMiBPerRun(t *testing.T) {
	_, tenant, request := uploadFixture(t)
	if err := tenant.SetStorageQuota(t.Context(), StorageQuota{Tier: storageTierFree}); err != nil {
		t.Fatal(err)
	}
	if freeTierQuota.MaxBytesPerRun != 100<<20 {
		t.Fatalf("free tier per-run limit = %d; want 100 MiB", freeTierQuota.MaxBytesPerRun)
	}
	request.Intent.Size = 100<<20 + 1
	var refusal *StorageQuotaError
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) || refusal.Limit != storageLimitBytesPerRun || refusal.Tier != storageTierFree {
		t.Fatalf("user data past 100 MiB = %v; want a free-tier bytes_per_run refusal", err)
	}
	request.Intent.Size = 100 << 20
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatalf("user data at 100 MiB refused: %v", err)
	}
}
