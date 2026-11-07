package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// A free-tier team publishes a prepared pipeline larger than its per-run limit without spending that limit; one past
// the prepared pipeline cap is refused with the cap and the fix, and a repeated digest stores no second copy.
func TestPreparedPipelineSkipsTheStorageQuotaUnderItsOwnCap(t *testing.T) {
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
	reserve := func(id, digest string, size int64) (UploadReservation, error) {
		return tenant.ReserveBlobUpload(t.Context(), UploadReservationRequest{ID: id, Intent: blob.UploadIntent{Authority: authority, Identity: identity, Digest: blob.Digest(strings.Repeat(digest, 64)), Size: size}, ExpiresAt: s.now().Add(time.Hour)})
	}
	const preparedSize = 30 << 20
	for _, id := range []string{"prepared", "again"} {
		ticket, err := reserve(id, "a", preparedSize)
		if err != nil {
			t.Fatalf("%s: free tier refused a 30 MB prepared pipeline: %v", id, err)
		}
		if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, authority)); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaCount(t, s.db, "blobs", 1)
	usage, err := tenant.StorageUsageFor(t.Context(), "run", StorageMonth(s.now()))
	if err != nil || usage.RunBytes != 0 || usage.RunObjects != 0 || usage.MonthBytes != 0 || usage.MonthObjects != 0 {
		t.Fatalf("prepared pipeline spent the storage quota: %+v: %v", usage, err)
	}
	var used int64
	if err := s.db.QueryRow("SELECT COALESCE(SUM(used+reserved),0) FROM quota_admission WHERE team='a'").Scan(&used); err != nil || used != 0 {
		t.Fatalf("prepared pipeline held quota %d: %v", used, err)
	}
	_, err = reserve("huge", "b", limits.PreparedPipelineBytes+1)
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.PayloadTooLarge {
		t.Fatalf("oversized prepared pipeline = %v; want payload_too_large", err)
	}
	for _, want := range []string{"256 MiB", "prepared pipeline", "trimming its dependencies"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("oversized refusal lacks %q: %v", want, err)
		}
	}
	if _, err := reserve("at_cap", "c", limits.PreparedPipelineBytes); err != nil {
		t.Fatalf("prepared pipeline at the cap refused: %v", err)
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
