package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedRunWithNode(t *testing.T, st *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node %s/%s: %v", runID, nodeID, err)
	}
}

func appendEventAt(t *testing.T, st *store.Store, runID, nodeID, kind string, at time.Time) {
	t.Helper()
	seq, err := st.AppendEvent(context.Background(), runID, nodeID, kind, nil)
	if err != nil {
		t.Fatalf("append event %s: %v", kind, err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`UPDATE events SET ts = ? WHERE run_id = ? AND seq = ?`), at.UnixNano(), runID, seq); err != nil {
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

func TestSweepRetentionRemovesOnlyRowsPastTheWindow(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1")
	now := time.Now().UTC()

	appendEventAt(t, st, "r1", "n1", "old", now.Add(-40*24*time.Hour))
	appendEventAt(t, st, "r1", "n1", "new", now.Add(-time.Hour))
	for _, ts := range []time.Time{now.Add(-40 * 24 * time.Hour), now.Add(-time.Hour)} {
		if err := st.AddNodeMetricSample(ctx, "r1", "n1", store.MetricSample{
			TS: ts, CPUMillicores: 10, MemoryBytes: 20,
		}); err != nil {
			t.Fatalf("add metric at %s: %v", ts, err)
		}
	}

	if err := st.SetStorageSettings(ctx, store.StorageSettings{
		EventRetentionDays: 30, NodeMetricRetentionDays: 14,
	}); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	swept, err := st.SweepRetention(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Events != 1 || swept.NodeMetrics != 1 {
		t.Fatalf("swept events=%d metrics=%d, want 1 and 1", swept.Events, swept.NodeMetrics)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM events`); got != 1 {
		t.Fatalf("events left = %d, want 1", got)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM node_metrics`); got != 1 {
		t.Fatalf("metric samples left = %d, want 1", got)
	}
	events, err := st.ListEventsAfter(ctx, "r1", 0, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "new" {
		t.Fatalf("surviving events = %+v, want only the recent one", events)
	}
}

func TestSweepRetentionLeavesTheRunAndItsForeignKeysIntact(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1")
	now := time.Now().UTC()
	appendEventAt(t, st, "r1", "n1", "old", now.Add(-40*24*time.Hour))
	if err := st.AddNodeMetricSample(ctx, "r1", "n1", store.MetricSample{
		TS: now.Add(-40 * 24 * time.Hour), CPUMillicores: 1, MemoryBytes: 2,
	}); err != nil {
		t.Fatalf("add metric: %v", err)
	}
	if err := st.SetStorageSettings(ctx, store.StorageSettings{
		EventRetentionDays: 30, NodeMetricRetentionDays: 30,
	}); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	if _, err := st.SweepRetention(ctx, now); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := st.GetRun(ctx, "r1"); err != nil {
		t.Fatalf("run after sweep: %v", err)
	}
	if _, err := st.GetNode(ctx, "r1", "n1"); err != nil {
		t.Fatalf("node after sweep: %v", err)
	}
	if err := st.AddNodeMetricSample(ctx, "r1", "n1", store.MetricSample{
		TS: now, CPUMillicores: 3, MemoryBytes: 4,
	}); err != nil {
		t.Fatalf("write a metric after the sweep: %v", err)
	}
	if err := st.DeleteRun(ctx, "r1"); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM node_metrics`); got != 0 {
		t.Fatalf("metric rows after deleting the run = %d, want 0; the cascade did not hold", got)
	}
}

func TestSweepRetentionRemovesNothingWithoutAWindow(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1")
	now := time.Now().UTC()
	appendEventAt(t, st, "r1", "n1", "ancient", now.Add(-9999*time.Hour))
	swept, err := st.SweepRetention(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Events != 0 || swept.NodeMetrics != 0 {
		t.Fatalf("swept %+v on a store with no retention set, want nothing", swept)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM events`); got != 1 {
		t.Fatalf("events left = %d, want 1", got)
	}
}

func TestDatabaseSizeReportsBytes(t *testing.T) {
	st := storetest.Open(t)
	size, err := st.DatabaseSize(context.Background())
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size.TotalBytes <= 0 {
		t.Fatalf("total bytes = %d, want a positive size", size.TotalBytes)
	}
	if size.SampledAt.IsZero() {
		t.Fatal("the sample carries no time")
	}
	if st.Dialect() == store.DialectPostgres && len(size.Tables) == 0 {
		t.Fatal("postgres reported no per-table sizes")
	}
}

func TestStorageSettingsRoundTrip(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	zero, err := st.StorageSettings(ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if zero.RetentionOn() || zero.DefaultTier != "" {
		t.Fatalf("a fresh store reads %+v, want retention off and no default tier", zero)
	}
	want := store.StorageSettings{
		EventRetentionDays:      store.CloudEventRetentionDays,
		NodeMetricRetentionDays: store.CloudNodeMetricRetentionDays,
		BackupRetentionDays:     store.CloudBackupRetentionDays,
		DatabaseAlarmBytes:      1 << 30,
		BackupAlarmBytes:        2 << 30,
		DefaultTier:             store.StorageTierFree,
	}
	if err := st.SetStorageSettings(ctx, want); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	got, err := st.StorageSettings(ctx)
	if err != nil {
		t.Fatalf("reread settings: %v", err)
	}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
	if err := st.SetStorageSettings(ctx, store.StorageSettings{DefaultTier: "platinum"}); err == nil {
		t.Fatal("an unknown default tier was accepted")
	}
}

func TestStorageQuotaFallsBackToTheDefaultTier(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	open, err := st.StorageQuotaFor(ctx, "alice")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if !open.Unlimited() {
		t.Fatalf("quota %+v on a store with no tier set, want unlimited", open)
	}
	if err := st.SetStorageSettings(ctx, store.StorageSettings{DefaultTier: store.StorageTierFree}); err != nil {
		t.Fatalf("set default tier: %v", err)
	}
	free, err := st.StorageQuotaFor(ctx, "alice")
	if err != nil {
		t.Fatalf("quota after the default: %v", err)
	}
	if free.MaxBytesPerRun != store.FreeTierQuota.MaxBytesPerRun || free.Principal != "alice" {
		t.Fatalf("quota = %+v, want the free tier for alice", free)
	}
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "alice", Tier: store.StorageTierPaid,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	paid, err := st.StorageQuotaFor(ctx, "alice")
	if err != nil {
		t.Fatalf("quota after the row: %v", err)
	}
	if paid.MaxBytesPerRun != store.PaidTierQuota.MaxBytesPerRun {
		t.Fatalf("quota = %+v, want the paid tier limits", paid)
	}
	rows, err := st.ListStorageQuotas(ctx)
	if err != nil {
		t.Fatalf("list quotas: %v", err)
	}
	if len(rows) != 1 || rows[0].Principal != "alice" {
		t.Fatalf("quota rows = %+v, want one for alice", rows)
	}
}

func TestReserveStorageRefusesEachLimit(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name    string
		quota   store.StorageQuota
		bytes   int64
		objects int64
		limit   string
	}{
		{"bytes per run", store.StorageQuota{Principal: "alice", MaxBytesPerRun: 100}, 101, 0, store.StorageLimitBytesPerRun},
		{"bytes per month", store.StorageQuota{Principal: "alice", MaxBytesPerMonth: 100}, 101, 0, store.StorageLimitBytesPerMonth},
		{"objects per run", store.StorageQuota{Principal: "alice", MaxObjectsPerRun: 2}, 0, 3, store.StorageLimitObjectsPerRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			ctx := context.Background()
			if err := st.SetStorageQuota(ctx, tc.quota); err != nil {
				t.Fatalf("set quota: %v", err)
			}
			err := st.ReserveStorage(ctx, "alice", "r1", tc.bytes, tc.objects, now)
			if !errors.Is(err, store.ErrStorageQuota) {
				t.Fatalf("reserve error = %v, want a quota refusal", err)
			}
			var refusal *store.StorageQuotaError
			if !errors.As(err, &refusal) {
				t.Fatalf("error %v does not carry the refusal detail", err)
			}
			if refusal.Limit != tc.limit {
				t.Fatalf("refusal names %q, want %q", refusal.Limit, tc.limit)
			}
			usage, uerr := st.StorageUsageFor(ctx, "alice", "r1", now)
			if uerr != nil {
				t.Fatalf("usage: %v", uerr)
			}
			if usage.RunBytes != 0 || usage.RunObjects != 0 {
				t.Fatalf("a refused write was counted: %+v", usage)
			}
		})
	}
}

func TestReserveStorageAccumulatesUnderTheLimit(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "alice", MaxBytesPerRun: 100, MaxBytesPerMonth: 150, MaxObjectsPerRun: 5,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	for range 2 {
		if err := st.ReserveStorage(ctx, "alice", "r1", 40, 1, now); err != nil {
			t.Fatalf("reserve: %v", err)
		}
	}
	usage, err := st.StorageUsageFor(ctx, "alice", "r1", now)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.RunBytes != 80 || usage.RunObjects != 2 || usage.MonthBytes != 80 {
		t.Fatalf("usage = %+v, want 80 bytes and 2 objects", usage)
	}
	if err := st.ReserveStorage(ctx, "alice", "r1", 40, 0, now); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("the third write returned %v, want a per-run refusal", err)
	}
	if err := st.ReserveStorage(ctx, "alice", "r2", 40, 0, now); err != nil {
		t.Fatalf("a second run under the month limit: %v", err)
	}
	if err := st.ReserveStorage(ctx, "alice", "r3", 40, 0, now); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("a third run past the month limit returned %v, want a refusal", err)
	}
	top, err := st.TopStorageTeams(ctx, store.StorageMonth(now), 5)
	if err != nil {
		t.Fatalf("top teams: %v", err)
	}
	if len(top) != 1 || top[0].Principal != "alice" || top[0].Bytes != 120 || top[0].Runs != 2 {
		t.Fatalf("top teams = %+v, want alice with 120 bytes over 2 runs", top)
	}
}

func TestReserveStorageWritesNothingWithoutAQuota(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.ReserveStorage(ctx, "alice", "r1", 1<<30, 1000, now); err != nil {
		t.Fatalf("reserve without a quota: %v", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM storage_usage`); got != 0 {
		t.Fatalf("usage rows = %d, want none while quotas are off", got)
	}
}
