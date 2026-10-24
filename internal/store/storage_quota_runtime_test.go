package store

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStoreDataQuotaConfigurationIsTenantScoped(t *testing.T) {
	s := authorityStore(t)
	a, b := &Tenant{s: s, team: "a"}, &Tenant{s: s, team: "b"}
	ctx := t.Context()
	if err := a.SetStorageQuota(ctx, StorageQuota{MaxBytesPerMonth: 123}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetStorageAllowance(ctx, 456); err != nil {
		t.Fatal(err)
	}
	if err := a.SetStorageQuota(ctx, StorageQuota{Tier: storageTierFree}); err != nil {
		t.Fatal(err)
	}
	got, err := a.StorageQuota(ctx)
	if err != nil || got.Team != "a" || got.AllowanceBytes != 456 || got.MaxBytesPerRun != freeTierQuota.MaxBytesPerRun || got.MaxBytesPerMonth != freeTierQuota.MaxBytesPerMonth {
		t.Fatalf("quota=%+v: %v", got, err)
	}
	if err := a.SetStorageQuota(ctx, StorageQuota{Team: "b", MaxBytesPerMonth: 1}); err == nil {
		t.Fatal("foreign quota accepted")
	}
	if _, err := a.SetStorageAllowance(ctx, -1); err == nil {
		t.Fatal("negative allowance accepted")
	}
	for _, q := range []StorageQuota{{Tier: "unknown"}, {MaxBytesPerRun: -1}, {MaxBytesPerMonth: -1}, {MaxObjectsPerRun: -1}} {
		if err := a.SetStorageQuota(ctx, q); err == nil {
			t.Fatalf("invalid quota accepted: %+v", q)
		}
	}
	other, err := b.StorageQuota(ctx)
	if err != nil || other != (StorageQuota{Team: "b"}) {
		t.Fatalf("other quota=%+v: %v", other, err)
	}
	quotas, err := s.AsOperator().ListStorageQuotas(ctx)
	if err != nil || !reflect.DeepEqual(quotas, []StorageQuota{got}) {
		t.Fatalf("operator quotas=%+v: %v", quotas, err)
	}
}

func TestStoreDataUsageReportSumsReplicaAndPeriodRows(t *testing.T) {
	s := authorityStore(t)
	a := &Tenant{s: s, team: "a"}
	ctx := t.Context()
	for _, row := range []struct {
		team, period, replica, run string
		bytes, objects             int64
	}{
		{"a", "2026-10", "r1", "run", 7, 1},
		{"a", "2026-10", "r2", "run", 11, 2},
		{"a", "2026-09", "r1", "run", 13, 3},
		{"b", "2026-10", "r1", "run", 1000, 4},
	} {
		rewriteSchemaExec(t, s.db, "INSERT INTO storage_run_usage(team,period,replica_id,run_id,bytes,objects,updated_at) VALUES (?,?,?,?,?,?,1)", row.team, row.period, row.replica, row.run, row.bytes, row.objects)
		rewriteSchemaExec(t, s.db, "INSERT INTO storage_month_usage(team,period,replica_id,bytes,objects,updated_at) VALUES (?,?,?,?,?,1)", row.team, row.period, row.replica, row.bytes, row.objects)
	}
	got, err := a.StorageUsageFor(ctx, "run", "2026-10")
	want := StorageUsage{Team: "a", RunID: "run", Month: "2026-10", RunBytes: 31, RunObjects: 6, MonthBytes: 18, MonthObjects: 3}
	if err != nil || got != want {
		t.Fatalf("usage=%+v, want %+v: %v", got, want, err)
	}
	empty, err := a.StorageUsageFor(ctx, "missing", "2026-08")
	if err != nil || empty != (StorageUsage{Team: "a", RunID: "missing", Month: "2026-08"}) {
		t.Fatalf("empty=%+v: %v", empty, err)
	}
	top, err := s.AsOperator().TopStorageTeams(ctx, "2026-10", 1)
	if err != nil || !reflect.DeepEqual(top, []StorageTeamUsage{{Team: "b", Month: "2026-10", Bytes: 1000, Objects: 4}}) {
		t.Fatalf("ranking=%+v: %v", top, err)
	}
}

func TestStoreDataRetainedReportCountsUniquePhysicalInventory(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	if err := tenant.RecordSegments(t.Context(), logCommit(actor, attempt)); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,7,1),('b',?,100,1)", strings.Repeat("a", 64), strings.Repeat("a", 64))
	rewriteSchemaExec(t, s.db, "INSERT INTO storage_month_usage(team,period,replica_id,bytes,updated_at) VALUES ('a','old','r1',1000,1),('a','old','r2',1000,1)")
	got, err := tenant.StorageRetainedBytes(t.Context())
	if err != nil || got != 10 {
		t.Fatalf("retained=%d: %v", got, err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
	got, err = tenant.StorageRetainedBytes(t.Context())
	if err != nil || got != 7 {
		t.Fatalf("retained after release=%d: %v", got, err)
	}
}

func TestStoreDataQuotaReportRejectsOverflowAndCorruption(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	rewriteSchemaExec(t, s.db, "INSERT INTO storage_month_usage(team,period,replica_id,bytes,updated_at) VALUES ('a','period','r1',?,1),('a','period','r2',1,1)", int64(math.MaxInt64))
	if _, err := tenant.StorageUsageFor(t.Context(), "", "period"); err == nil {
		t.Fatal("overflowing usage accepted")
	}
	rewriteSchemaExec(t, s.db, "PRAGMA ignore_check_constraints=ON")
	rewriteSchemaExec(t, s.db, "UPDATE storage_month_usage SET bytes=-1 WHERE team='a' AND replica_id='r2'")
	if _, err := tenant.StorageUsageFor(t.Context(), "", "period"); err == nil {
		t.Fatal("negative counter concealed in sum")
	}
	if _, err := s.AsOperator().TopStorageTeams(t.Context(), "period", 10); err == nil {
		t.Fatal("negative counter concealed in ranking")
	}
	rewriteSchemaExec(t, s.db, "INSERT INTO storage_quotas(team,max_bytes_per_month,updated_at) VALUES ('a',-1,1)")
	if _, err := tenant.StorageQuota(t.Context()); err == nil {
		t.Fatal("negative quota accepted")
	}
}

func TestStoreDataQuotaAllowanceConcurrentWritersAndRestart(t *testing.T) {
	s := authorityStore(t)
	path := blobAccountingDiskStore(t, s)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
	a, b := &Tenant{s: s, team: "a"}, &Tenant{s: other, team: "a"}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- a.SetStorageQuota(t.Context(), StorageQuota{MaxBytesPerMonth: 123}) }()
	go func() { <-start; _, err := b.SetStorageAllowance(t.Context(), 456); results <- err }()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.StorageQuota(t.Context())
	if err != nil || got.MaxBytesPerMonth != 123 || got.AllowanceBytes != 456 {
		t.Fatalf("concurrent fields=%+v: %v", got, err)
	}
	if err := other.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := &Tenant{s: &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: func() time.Time { return time.Now() }}, team: "a"}
	if after, err := reopened.StorageQuota(t.Context()); err != nil || after != got {
		t.Fatalf("restart=%+v: %v", after, err)
	}
}
