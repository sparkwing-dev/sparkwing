package store

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
)

func TestStoreDataEventsUseFinalReplicaRows(t *testing.T) {
	s, tenant, _, _ := resultFixture(t)
	if err := tenant.AppendEvent(t.Context(), "run", "job", "note", []byte("abc")); err != nil {
		t.Fatalf("final-schema event append: %v", err)
	}
	usage, err := tenant.StorageUsageFor(t.Context(), "run", StorageMonth(s.now()))
	if err != nil || usage.RunBytes != 7 || usage.MonthBytes != 7 || usage.RunObjects != 0 || usage.MonthObjects != 0 {
		t.Fatalf("event usage=%+v: %v", usage, err)
	}
	standing, err := tenant.StorageStanding(t.Context())
	if err != nil || standing.EventBytes != 7 {
		t.Fatalf("retained events=%+v: %v", standing, err)
	}
	rewriteSchemaCount(t, s.db, "quota_reservations", 0)
}

func TestStoreDataEventsRefuseEachLimitAtomically(t *testing.T) {
	for _, tc := range []struct {
		limit string
		quota StorageQuota
		caps  eventLimits
	}{
		{storageLimitBytesPerRun, StorageQuota{MaxBytesPerRun: 1}, eventLimits{}},
		{storageLimitBytesPerMonth, StorageQuota{MaxBytesPerMonth: 1}, eventLimits{}},
		{storageLimitEventBytes, StorageQuota{}, eventLimits{MaxBytesPerEvent: 1}},
		{storageLimitEventBytesPerRun, StorageQuota{}, eventLimits{MaxBytesPerRun: 1}},
		{storageLimitEventsPerRun, StorageQuota{}, eventLimits{MaxEventsPerRun: 1}},
	} {
		t.Run(tc.limit, func(t *testing.T) {
			s, tenant, _, _ := resultFixture(t)
			if err := tenant.SetStorageQuota(t.Context(), tc.quota); err != nil {
				t.Fatal(err)
			}
			if err := s.setEventLimits(t.Context(), tc.caps); err != nil {
				t.Fatal(err)
			}
			if tc.limit == storageLimitEventsPerRun {
				if err := tenant.AppendEvent(t.Context(), "run", "", "k", nil); err != nil {
					t.Fatal(err)
				}
			}
			before := eventAccountingSnapshot(t, s)
			err := tenant.AppendEvent(t.Context(), "run", "", "kind", nil)
			var refusal *StorageQuotaError
			if !errors.As(err, &refusal) || refusal.Limit != tc.limit || refusal.Principal != "a" {
				t.Fatalf("event: %v, want %s", err, tc.limit)
			}
			if after := eventAccountingSnapshot(t, s); after != before {
				t.Fatalf("refused event changed accounting: before=%s after=%s", before, after)
			}
		})
	}
}

func eventAccountingSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var totals [6]int64
	queries := []string{
		"SELECT COUNT(*) FROM events",
		"SELECT COALESCE(SUM(event_bytes),0) FROM runs",
		"SELECT COALESCE(SUM(event_count),0) FROM runs",
		"SELECT COALESCE(SUM(used+reserved),0) FROM quota_admission",
		"SELECT COALESCE(SUM(bytes),0) FROM storage_month_usage",
		"SELECT COALESCE(SUM(bytes),0) FROM storage_run_usage",
	}
	for i, query := range queries {
		if err := s.db.QueryRow(query).Scan(&totals[i]); err != nil {
			t.Fatal(err)
		}
	}
	return fmt.Sprint(totals)
}

func TestStoreDataEventsShareCapacityWithUploadReservations(t *testing.T) {
	for _, limit := range []string{storageLimitBytesPerMonth, storageLimitBytesPerRun} {
		t.Run(limit, func(t *testing.T) {
			_, tenant, request := uploadFixture(t)
			quota := StorageQuota{MaxBytesPerRun: 4}
			if limit == storageLimitBytesPerMonth {
				quota = StorageQuota{MaxBytesPerMonth: 4}
			}
			if err := tenant.SetStorageQuota(t.Context(), quota); err != nil {
				t.Fatal(err)
			}
			ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			var refusal *StorageQuotaError
			if err := tenant.AppendEvent(t.Context(), "run", "", "kk", nil); !errors.As(err, &refusal) || refusal.Limit != limit || refusal.Used != 3 {
				t.Fatalf("event borrowed pending upload capacity: %v", err)
			}
			if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, ticket.ID); err != nil {
				t.Fatal(err)
			}
			if err := tenant.AppendEvent(t.Context(), "run", "", "kk", nil); err != nil {
				t.Fatal(err)
			}
			request.ID = "later"
			if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) || refusal.Limit != limit || refusal.Used != 2 {
				t.Fatalf("upload borrowed event capacity: %v", err)
			}
		})
	}
}

func TestStoreDataEventsMonthAndRunUsageSurviveRestart(t *testing.T) {
	s, tenant, _, _ := resultFixture(t)
	now := time.Date(2026, 1, 31, 23, 59, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	if err := tenant.SetStorageQuota(t.Context(), StorageQuota{MaxBytesPerRun: 7, MaxBytesPerMonth: 5}); err != nil {
		t.Fatal(err)
	}
	if err := tenant.AppendEvent(t.Context(), "run", "", "kind", nil); err != nil {
		t.Fatal(err)
	}
	path := blobAccountingDiskStore(t, s)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	now = now.Add(2 * time.Minute)
	if err := tenant.AppendEvent(t.Context(), "run", "", "kkk", nil); err != nil {
		t.Fatal(err)
	}
	var refusal *StorageQuotaError
	if err := tenant.AppendEvent(t.Context(), "run", "", "k", nil); !errors.As(err, &refusal) || refusal.Limit != storageLimitBytesPerRun || refusal.Used != 7 {
		t.Fatalf("new month reset run capacity: %v", err)
	}
	for month, want := range map[string]int64{"2026-01": 4, "2026-02": 3, "2025-12": 0} {
		usage, err := tenant.StorageUsageFor(t.Context(), "run", month)
		if err != nil || usage.MonthBytes != want || usage.RunBytes != 7 {
			t.Fatalf("month=%s usage=%+v: %v", month, usage, err)
		}
	}
}

func TestStoreDataEventsFailuresLeaveNoPartialCharge(t *testing.T) {
	for _, mode := range []string{"unknown_run", "foreign_run", "invalid_kind", "monthly_write", "run_write", "event_write", "quota_overflow", "usage_overflow", "event_overflow"} {
		t.Run(mode, func(t *testing.T) {
			s, tenant, _, _ := resultFixture(t)
			run, kind := "run", "kind"
			switch mode {
			case "unknown_run":
				run = "missing"
			case "foreign_run":
				tenant = &Tenant{s: s, team: "b"}
			case "invalid_kind":
				kind = "secret\nkind"
			case "monthly_write", "run_write", "event_write":
				table := map[string]string{"monthly_write": "storage_month_usage", "run_write": "storage_run_usage", "event_write": "events"}[mode]
				rewriteSchemaExec(t, s.db, "CREATE TRIGGER refuse_event BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'unavailable'); END")
			case "quota_overflow":
				rewriteSchemaExec(t, s.db, "INSERT INTO quota_admission(team,quota,period,used,updated_at) VALUES ('a','storage',?,?,1)", StorageMonth(s.now()), int64(math.MaxInt64))
			case "usage_overflow":
				rewriteSchemaExec(t, s.db, "INSERT INTO storage_month_usage(team,period,bytes,updated_at) VALUES ('a',?,?,1)", StorageMonth(s.now()), int64(math.MaxInt64))
			case "event_overflow":
				rewriteSchemaExec(t, s.db, "UPDATE runs SET event_bytes=?", int64(math.MaxInt64))
			}
			before := eventAccountingSnapshot(t, s)
			if err := tenant.AppendEvent(t.Context(), run, "", kind, nil); err == nil {
				t.Fatalf("accepted %s event: %v", mode, err)
			}
			if after := eventAccountingSnapshot(t, s); after != before {
				t.Fatalf("failure changed accounting: before=%s after=%s", before, after)
			}
		})
	}
}

func TestStoreDataEventsTenantIsolationAndConservation(t *testing.T) {
	property := func(first, second uint8) bool {
		s, tenant, _, _ := resultFixture(t)
		path := blobAccountingDiskStore(t, s)
		other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
		foreign := &Tenant{s: other, team: "b"}
		before := eventAccountingSnapshot(t, s)
		if err := foreign.AppendEvent(t.Context(), "run", "job", "k", nil); err == nil || eventAccountingSnapshot(t, s) != before {
			return false
		}
		amounts := []int{int(first) + 1, int(second) + 1}
		tenants := []*Tenant{tenant, {s: other, team: "a"}}
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for i, handle := range tenants {
			wg.Go(func() {
				err := handle.AppendEvent(t.Context(), "run", "job", "k", bytes.Repeat([]byte("x"), amounts[i]-1))
				results <- err
			})
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Log(err)
				return false
			}
		}
		want := int64(amounts[0] + amounts[1])
		usage, err := tenant.StorageUsageFor(t.Context(), "run", StorageMonth(s.now()))
		var retained, charged int64
		if err != nil || usage.RunBytes != want || usage.MonthBytes != want {
			return false
		}
		if err := s.db.QueryRow("SELECT event_bytes FROM runs WHERE team='a' AND id='run'").Scan(&retained); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("SELECT used FROM quota_admission WHERE team='a' AND quota='storage'").Scan(&charged); err != nil {
			t.Fatal(err)
		}
		return retained == want && charged == want
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(13711))})); err != nil {
		t.Fatal(err)
	}
}
