package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedRunWithNode(t *testing.T, st *store.Store, runID, nodeID, status string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "p", Status: status, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node %s/%s: %v", runID, nodeID, err)
	}
}

func appendEventAt(t *testing.T, st *store.Store, runID, nodeID, kind string, at time.Time) {
	t.Helper()
	if err := st.AppendEvent(context.Background(), runID, nodeID, kind, nil); err != nil {
		t.Fatalf("append event %s: %v", kind, err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`UPDATE events SET ts = ? WHERE run_id = ? AND seq = (SELECT MAX(seq) FROM events WHERE run_id = ?)`), at.UnixNano(), runID, runID); err != nil {
		t.Fatalf("backdate event %s: %v", kind, err)
	}
}

func countRows(t *testing.T, st *store.Store, query string) int64 {
	t.Helper()
	var n int64
	if err := st.DB().QueryRow(storetest.Rebind(st, query)).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

func TestChargedEventRefusesEachLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		quota   store.StorageQuota
		payload int
		limit   string
	}{
		{"bytes per run", store.StorageQuota{Team: store.DefaultTeam, MaxBytesPerRun: 8}, 9, store.StorageLimitBytesPerRun},
		{"bytes per month", store.StorageQuota{Team: store.DefaultTeam, MaxBytesPerMonth: 8}, 9, store.StorageLimitBytesPerMonth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			ctx := context.Background()
			seedRunWithNode(t, st, "r1", "n1", "running")
			if err := storageTestTenant(t, st).SetStorageQuota(ctx, tc.quota); err != nil {
				t.Fatalf("set quota: %v", err)
			}
			payload := make([]byte, tc.payload)
			_, err := st.AppendEventCharged(ctx, "alice", "r1", "n1", "note", payload)
			if !errors.Is(err, store.ErrStorageQuota) {
				t.Fatalf("charge error = %v, want a quota refusal", err)
			}
			var refusal *store.StorageQuotaError
			if !errors.As(err, &refusal) {
				t.Fatalf("error %v does not carry the refusal detail", err)
			}
			if refusal.Limit != tc.limit {
				t.Fatalf("refusal names %q, want %q", refusal.Limit, tc.limit)
			}
			if got := countRows(t, st, `SELECT COUNT(*) FROM events`); got != 0 {
				t.Fatalf("a refused event wrote %d rows, want 0", got)
			}
			usage, uerr := storageTestTenant(t, st).StorageUsageFor(ctx, "r1", store.StorageMonth(time.Now().UTC()))
			if uerr != nil {
				t.Fatalf("usage: %v", uerr)
			}
			if usage.RunBytes != 0 || usage.MonthBytes != 0 {
				t.Fatalf("a refused write was charged: %+v", usage)
			}
		})
	}
}

func TestChargedWriteAccumulatesUnderTheLimit(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "running")
	seedRunWithNode(t, st, "r2", "n1", "running")
	month := store.StorageMonth(time.Now().UTC())
	if err := storageTestTenant(t, st).SetStorageQuota(ctx, store.StorageQuota{
		Team: store.DefaultTeam, MaxBytesPerRun: 40, MaxBytesPerMonth: 60,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	payload := make([]byte, 15)
	for i := range 2 {
		if _, err := st.AppendEventCharged(ctx, "alice", "r1", "n1", "k", payload); err != nil {
			t.Fatalf("charge %d: %v", i, err)
		}
	}
	usage, err := storageTestTenant(t, st).StorageUsageFor(ctx, "r1", month)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.RunBytes != 32 || usage.MonthBytes != 32 {
		t.Fatalf("usage = %+v, want 32 bytes charged to the run and the month", usage)
	}
	if _, err := st.AppendEventCharged(ctx, "alice", "r1", "n1", "k", payload); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("the third write on r1 returned %v, want a per-run refusal", err)
	}
	if _, err := st.AppendEventCharged(ctx, "alice", "r2", "n1", "k", payload); err != nil {
		t.Fatalf("a second run under the month limit: %v", err)
	}
	if _, err := st.AppendEventCharged(ctx, "alice", "r2", "n1", "k", payload); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("a write past the month limit returned %v, want a refusal", err)
	}
	top, err := st.AsOperator().TopStorageTeams(ctx, month, 5)
	if err != nil {
		t.Fatalf("top teams: %v", err)
	}
	if len(top) != 1 || top[0].Team != store.DefaultTeam || top[0].Bytes != 48 {
		t.Fatalf("top teams = %+v, want the default team with 48 bytes", top)
	}
}

func TestChargedWriteIsBilledToTheMonthItIsWrittenIn(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "running")
	if err := storageTestTenant(t, st).SetStorageQuota(ctx, store.StorageQuota{
		Team: store.DefaultTeam, MaxBytesPerMonth: 64,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	now := time.Now().UTC()
	if _, err := st.AppendEventCharged(ctx, "alice", "r1", "n1", "note", make([]byte, 16)); err != nil {
		t.Fatalf("charge: %v", err)
	}
	thisMonth, err := storageTestTenant(t, st).StorageUsageFor(ctx, "r1", store.StorageMonth(now))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if thisMonth.MonthBytes != 20 {
		t.Fatalf("this month = %d bytes, want 20, kind and payload", thisMonth.MonthBytes)
	}
	last, err := storageTestTenant(t, st).StorageUsageFor(ctx, "r1", store.StorageMonth(now.AddDate(0, -1, 0)))
	if err != nil {
		t.Fatalf("usage for the previous month: %v", err)
	}
	if last.MonthBytes != 0 {
		t.Fatalf("the previous month reads %d bytes, want 0", last.MonthBytes)
	}
}

func TestChargedWriteCountsNothingWithoutAQuota(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "running")
	if _, err := st.AppendEventCharged(ctx, "alice", "r1", "n1", "note", make([]byte, 4096)); err != nil {
		t.Fatalf("charge without a quota: %v", err)
	}
	for _, table := range []string{"storage_run_usage", "storage_month_usage"} {
		if got := countRows(t, st, `SELECT COUNT(*) FROM `+table); got != 0 {
			t.Fatalf("%s holds %d rows, want none while quotas are off", table, got)
		}
	}
}

func TestChargedWriteRefusesARunThatDoesNotExist(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := storageTestTenant(t, st).SetStorageQuota(ctx, store.StorageQuota{
		Team: store.DefaultTeam, MaxBytesPerRun: 1024,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if _, err := st.AppendEventCharged(ctx, "alice", "ghost", "", "note", make([]byte, 8)); err == nil {
		t.Fatal("a charge against a run that does not exist was accepted")
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM storage_run_usage`); got != 0 {
		t.Fatalf("per-run usage rows = %d, want none for a run that does not exist", got)
	}
}

func storageTestTenant(t *testing.T, s *store.Store) *store.Tenant {
	t.Helper()
	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	return tenant
}
