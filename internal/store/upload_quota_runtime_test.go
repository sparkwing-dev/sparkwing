package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestBlobUploadRunByteLimitIncludesPendingTransfers(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	if err := tenant.SetStorageQuota(t.Context(), StorageQuota{MaxBytesPerRun: 4}); err != nil {
		t.Fatal(err)
	}
	first, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.ID = "second"
	request.Intent.Digest = blob.Digest(strings.Repeat("b", 64))
	var refusal *StorageQuotaError
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) || refusal.Limit != storageLimitBytesPerRun || refusal.Used != 3 || refusal.Requested != 3 {
		t.Fatalf("pending transfer did not spend the run's byte capacity: %v", err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(first, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) {
		t.Fatalf("committed transfer did not spend the run's byte capacity: %v", err)
	}
	usage, err := tenant.StorageUsageFor(t.Context(), "run", StorageMonth(s.now()))
	if err != nil || usage.RunBytes != 3 || usage.RunObjects != 1 {
		t.Fatalf("failed admission changed usage: %+v: %v", usage, err)
	}
}

func TestBlobUploadRunObjectLimitCountsEmptyBlobsAndReleasesAborts(t *testing.T) {
	_, tenant, request := uploadFixture(t)
	if err := tenant.SetStorageQuota(t.Context(), StorageQuota{MaxObjectsPerRun: 1}); err != nil {
		t.Fatal(err)
	}
	request.Intent.Size = 0
	first, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.ID = "second"
	request.Intent.Digest = blob.Digest(strings.Repeat("b", 64))
	var refusal *StorageQuotaError
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) || refusal.Limit != storageLimitObjectsPerRun || refusal.Used != 1 || refusal.Requested != 1 {
		t.Fatalf("empty upload did not reserve an object: %v", err)
	}
	for range 2 {
		if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, first.ID); err != nil {
			t.Fatal(err)
		}
	}
	second, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatalf("abort did not release object capacity: %v", err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(second, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	request.ID = "known_empty_duplicate"
	duplicate, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatalf("authorized empty duplicate refused at object ceiling: %v", err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(duplicate, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	request.ID = "third"
	request.Intent.Digest = blob.Digest(strings.Repeat("c", 64))
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) || refusal.Used != 1 {
		t.Fatalf("new object admitted at ceiling: %v", err)
	}
}

func TestBlobUploadRunLimitsSurviveMonthChangeAndRestart(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	if err := tenant.SetStorageQuota(t.Context(), StorageQuota{MaxBytesPerRun: 4, MaxObjectsPerRun: 2}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 31, 23, 59, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	request.ExpiresAt = now.Add(time.Hour)
	first, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	path := blobAccountingDiskStore(t, s)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	now = now.Add(2 * time.Minute)
	request.ID = "february"
	request.Intent.Digest = blob.Digest(strings.Repeat("b", 64))
	var refusal *StorageQuotaError
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) || refusal.Limit != storageLimitBytesPerRun {
		t.Fatalf("month change forgot the previous month's pending bytes: %v", err)
	}
	request.Intent.Size = 1
	second, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for _, ticket := range []UploadReservation{first, second} {
			if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil {
				t.Fatal(err)
			}
		}
	}
	usage, err := tenant.StorageUsageFor(t.Context(), "run", "2026-02")
	if err != nil || usage.RunBytes != 4 || usage.RunObjects != 2 || usage.MonthBytes != 1 || usage.MonthObjects != 1 {
		t.Fatalf("rollover or replay changed usage: %+v: %v", usage, err)
	}
	request.ID = "over_limit"
	request.Intent.Digest = blob.Digest(strings.Repeat("c", 64))
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) {
		t.Fatalf("reopened run exceeded its lifetime quota: %v", err)
	}
}

func TestBlobUploadUsesEffectivePolicyWithoutCreatingQuotaOverrides(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	rewriteSchemaExec(t, s.db, "DELETE FROM storage_quotas WHERE team='a'")
	first, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatalf("unconfigured storage must remain unlimited: %v", err)
	}
	if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().SetStorageSettings(t.Context(), StorageSettings{DefaultTier: storageTierFree}); err != nil {
		t.Fatal(err)
	}
	request.ID = "default_tier"
	request.Intent.Size = freeTierQuota.MaxBytesPerRun + 1
	var refusal *StorageQuotaError
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) || refusal.Limit != storageLimitBytesPerRun {
		t.Fatalf("deployment default did not apply to upload: %v", err)
	}
	if err := s.AsOperator().SetStorageSettings(t.Context(), StorageSettings{DefaultTier: storageTierPaid}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatalf("first upload froze the deployment default: %v", err)
	}
	quotas, err := s.AsOperator().ListStorageQuotas(t.Context())
	if err != nil || len(quotas) != 0 {
		t.Fatalf("admission created an explicit quota override: %+v: %v", quotas, err)
	}
}

func TestBlobUploadRunLimitsSerializeIndependentClients(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	if err := tenant.SetStorageQuota(t.Context(), StorageQuota{MaxObjectsPerRun: 1}); err != nil {
		t.Fatal(err)
	}
	path := blobAccountingDiskStore(t, s)
	other := &Tenant{s: &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}, team: "a"}
	start := make(chan struct{})
	type result struct {
		ticket UploadReservation
		err    error
	}
	results := make(chan result, 2)
	for i, client := range []*Tenant{tenant, other} {
		input := request
		input.ID = []string{"first", "second"}[i]
		input.Intent.Digest = blob.Digest(strings.Repeat([]string{"b", "c"}[i], 64))
		go func() {
			<-start
			ticket, err := client.ReserveBlobUpload(t.Context(), input)
			results <- result{ticket, err}
		}()
	}
	close(start)
	var winner UploadReservation
	winners := 0
	for range 2 {
		got := <-results
		if got.err == nil {
			winner = got.ticket
			winners++
		} else if !errors.Is(got.err, ErrStorageQuota) {
			t.Error(got.err)
		}
	}
	if winners != 1 {
		t.Fatalf("run object capacity admitted %d writers, want one", winners)
	}
	if _, err := other.CommitBlobUpload(t.Context(), verifiedUpload(winner, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	usage, err := tenant.StorageUsageFor(t.Context(), "run", StorageMonth(s.now()))
	if err != nil || usage.RunBytes != 3 || usage.RunObjects != 1 {
		t.Fatalf("race double-counted storage: %+v: %v", usage, err)
	}
}

func TestBlobUploadExpiryReleasesOrphansAfterMonthlyHoldWasReleased(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	s.clock = func() time.Time { return request.ExpiresAt }
	generic, err := tenant.ReserveQuota(t.Context(), "storage", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.SettleQuota(t.Context(), generic.ID, 0); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "replica", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().MarkBlobTombstones(t.Context(), lease, 1); err != nil {
		t.Fatal(err)
	}
	var held, reserved int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM quota_reservations WHERE team='a' AND state='held'").Scan(&held); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT SUM(reserved) FROM quota_admission WHERE team='a'").Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if held != 0 || reserved != 0 {
		t.Fatalf("run deletion left held=%d reserved=%d", held, reserved)
	}
}

func TestBlobUploadEmptyDuplicateCannotFundMissingObject(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	request.Intent.Size = 0
	first, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(first, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	request.ID = "duplicate"
	duplicate, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM blobs WHERE team='a'")
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(duplicate, request.Intent.Authority)); err == nil {
		t.Fatal("zero object reservation funded a new empty blob")
	}
	rewriteSchemaCount(t, s.db, "blobs", 0)
}

func TestBlobUploadRecordPointersShareTheObjectBudget(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	setBlobRecordRef(t, s, request.Intent.Authority.CapabilityRef)
	request.Intent.Authority.Ref.Scope = assertedRef("main")
	if err := tenant.SetStorageQuota(t.Context(), StorageQuota{MaxObjectsPerRun: 1}); err != nil {
		t.Fatal(err)
	}
	ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'", []byte(`{"id":"job","memo":{}}`))
	records := &blobRecords{tenant: tenant}
	output := blobOutputRequest(request.Intent)
	for range 2 {
		if _, err := records.CommitRecord(t.Context(), output); err != nil {
			t.Fatal(err)
		}
		memo := output
		memo.Key.Kind, memo.Key.Name = blob.KindMemo, "output"
		memo.Key.Scope = blob.Scope{Repo: "gh:1", Ref: assertedRef("main"), Pipeline: "ci", Job: "job", Key: fixtureMemoHash}
		if _, err := records.CommitRecord(t.Context(), memo); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := tenant.StorageUsageFor(t.Context(), "run", StorageMonth(s.now()))
	if err != nil || usage.RunObjects != 1 || usage.MonthObjects != 1 || usage.RunBytes != 3 {
		t.Fatalf("record pointers consumed upload budget: %+v: %v", usage, err)
	}
	request.ID, request.Intent.Digest = "new_object", blob.Digest(strings.Repeat("d", 64))
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("new blob bypassed object budget: %v", err)
	}
}
