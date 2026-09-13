package store_test

import (
	"context"
	"errors"
	"fmt"
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
	seedRunWithNode(t, st, "r1", "n1", "success")
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

func TestSweepRetentionSparesARunThatIsStillGoing(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "running", "n1", "running")
	seedRunWithNode(t, st, "finished", "n1", "success")
	now := time.Now().UTC()
	appendEventAt(t, st, "running", "n1", "old", now.Add(-40*24*time.Hour))
	appendEventAt(t, st, "finished", "n1", "old", now.Add(-40*24*time.Hour))
	if err := st.SetStorageSettings(ctx, store.StorageSettings{EventRetentionDays: 30}); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	swept, err := st.SweepRetention(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Events != 1 {
		t.Fatalf("swept %d events, want only the finished run's", swept.Events)
	}
	left, err := st.ListEventsAfter(ctx, "running", 0, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(left) != 1 {
		t.Fatalf("the running run kept %d events, want 1", len(left))
	}
}

func TestSweepRetentionRemovesMoreThanOneBatch(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "success")
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour).UnixNano()
	rows := store.RetentionSweepBatch + 7
	for i := range rows {
		if _, err := st.DB().Exec(storetest.Rebind(st,
			`INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos)
             VALUES (?, ?, ?, 1, 1, 0)`), "r1", "n1", old+int64(i)); err != nil {
			t.Fatalf("seed metric %d: %v", i, err)
		}
	}
	if err := st.SetStorageSettings(ctx, store.StorageSettings{NodeMetricRetentionDays: 30}); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	swept, err := st.SweepRetention(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.NodeMetrics != int64(rows) {
		t.Fatalf("swept %d metric rows, want %d across more than one batch", swept.NodeMetrics, rows)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM node_metrics`); got != 0 {
		t.Fatalf("metric rows left = %d, want 0", got)
	}
}

func TestSweepRetentionLeavesTheRunAndItsForeignKeysIntact(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "success")
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
	seedRunWithNode(t, st, "r1", "n1", "success")
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

func TestDatabaseSizeFallsAfterASweep(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "success")
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour).UnixNano()
	for i := range 20000 {
		if _, err := st.DB().Exec(storetest.Rebind(st,
			`INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos)
             VALUES (?, ?, ?, 1, 1, 0)`), "r1", "n1", old+int64(i)); err != nil {
			t.Fatalf("seed metric %d: %v", i, err)
		}
	}
	before, err := st.DatabaseSize(ctx)
	if err != nil {
		t.Fatalf("size before: %v", err)
	}
	if before.TotalBytes <= 0 || before.SampledAt.IsZero() {
		t.Fatalf("size before = %+v, want a positive size and a sample time", before)
	}
	if err := st.SetStorageSettings(ctx, store.StorageSettings{NodeMetricRetentionDays: 30}); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	if _, err := st.SweepRetention(ctx, now); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	after, err := st.DatabaseSize(ctx)
	if err != nil {
		t.Fatalf("size after: %v", err)
	}
	if st.Dialect() == store.DialectPostgres {
		if len(after.Tables) == 0 {
			t.Fatal("postgres reported no per-table sizes")
		}
		return
	}
	if after.TotalBytes >= before.TotalBytes {
		t.Fatalf("size after the sweep = %d, want below %d; an alarm raised before a sweep would never clear",
			after.TotalBytes, before.TotalBytes)
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
		EventRetentionDays:      30,
		NodeMetricRetentionDays: 14,
		DatabaseAlarmBytes:      1 << 30,
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
	for _, bad := range []string{"", "has space", "semi;colon"} {
		if err := st.SetStorageQuota(ctx, store.StorageQuota{Principal: bad, Tier: store.StorageTierFree}); err == nil {
			t.Fatalf("principal %q was accepted", bad)
		}
	}
}

func TestChargedEventRefusesEachLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		quota   store.StorageQuota
		payload int
		limit   string
	}{
		{"bytes per run", store.StorageQuota{Principal: "alice", MaxBytesPerRun: 8}, 9, store.StorageLimitBytesPerRun},
		{"bytes per month", store.StorageQuota{Principal: "alice", MaxBytesPerMonth: 8}, 9, store.StorageLimitBytesPerMonth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			ctx := context.Background()
			seedRunWithNode(t, st, "r1", "n1", "running")
			if err := st.SetStorageQuota(ctx, tc.quota); err != nil {
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
			usage, uerr := st.StorageUsageFor(ctx, "alice", "r1", store.StorageMonth(time.Now().UTC()))
			if uerr != nil {
				t.Fatalf("usage: %v", uerr)
			}
			if usage.RunBytes != 0 || usage.MonthBytes != 0 {
				t.Fatalf("a refused write was charged: %+v", usage)
			}
		})
	}
}

func TestChargedManifestRefusesTheObjectLimit(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "running")
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "alice", MaxObjectsPerRun: 1,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if err := st.SetNodeArtifactManifestCharged(ctx, "alice", "r1", "n1", "sha256:a"); err != nil {
		t.Fatalf("the first manifest: %v", err)
	}
	err := st.SetNodeArtifactManifestCharged(ctx, "alice", "r1", "n1", "sha256:b")
	var refusal *store.StorageQuotaError
	if !errors.As(err, &refusal) || refusal.Limit != store.StorageLimitObjectsPerRun {
		t.Fatalf("the second manifest returned %v, want an objects-per-run refusal", err)
	}
	node, err := st.GetNode(ctx, "r1", "n1")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.ArtifactManifest != "sha256:a" {
		t.Fatalf("manifest = %q, want the first one; the refused write was committed", node.ArtifactManifest)
	}
}

func TestChargedWriteAccumulatesUnderTheLimit(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "running")
	seedRunWithNode(t, st, "r2", "n1", "running")
	month := store.StorageMonth(time.Now().UTC())
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "alice", MaxBytesPerRun: 40, MaxBytesPerMonth: 60,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	payload := make([]byte, 16)
	for i := range 2 {
		if _, err := st.AppendEventCharged(ctx, "alice", "r1", "n1", fmt.Sprintf("note%d", i), payload); err != nil {
			t.Fatalf("charge %d: %v", i, err)
		}
	}
	usage, err := st.StorageUsageFor(ctx, "alice", "r1", month)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.RunBytes != 32 || usage.MonthBytes != 32 {
		t.Fatalf("usage = %+v, want 32 bytes charged to the run and the month", usage)
	}
	if _, err := st.AppendEventCharged(ctx, "alice", "r1", "n1", "third", payload); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("the third write on r1 returned %v, want a per-run refusal", err)
	}
	if _, err := st.AppendEventCharged(ctx, "alice", "r2", "n1", "first", payload); err != nil {
		t.Fatalf("a second run under the month limit: %v", err)
	}
	if _, err := st.AppendEventCharged(ctx, "alice", "r2", "n1", "second", payload); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("a write past the month limit returned %v, want a refusal", err)
	}
	top, err := st.TopStorageTeams(ctx, month, 5)
	if err != nil {
		t.Fatalf("top teams: %v", err)
	}
	if len(top) != 1 || top[0].Principal != "alice" || top[0].Bytes != 48 {
		t.Fatalf("top teams = %+v, want alice with 48 bytes", top)
	}
}

func TestChargedWriteIsBilledToTheMonthItIsWrittenIn(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "running")
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "alice", MaxBytesPerMonth: 64,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	now := time.Now().UTC()
	if _, err := st.AppendEventCharged(ctx, "alice", "r1", "n1", "note", make([]byte, 16)); err != nil {
		t.Fatalf("charge: %v", err)
	}
	thisMonth, err := st.StorageUsageFor(ctx, "alice", "r1", store.StorageMonth(now))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if thisMonth.MonthBytes != 16 {
		t.Fatalf("this month = %d bytes, want 16", thisMonth.MonthBytes)
	}
	last, err := st.StorageUsageFor(ctx, "alice", "r1", store.StorageMonth(now.AddDate(0, -1, 0)))
	if err != nil {
		t.Fatalf("usage for the previous month: %v", err)
	}
	if last.MonthBytes != 0 {
		t.Fatalf("the previous month reads %d bytes, want 0", last.MonthBytes)
	}
}

func TestChargedRunUsageCascadesWithItsRun(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedRunWithNode(t, st, "r1", "n1", "running")
	month := store.StorageMonth(time.Now().UTC())
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "alice", MaxBytesPerRun: 1024, MaxBytesPerMonth: 1024,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if _, err := st.AppendEventCharged(ctx, "alice", "r1", "n1", "note", make([]byte, 32)); err != nil {
		t.Fatalf("charge: %v", err)
	}
	if err := st.DeleteRun(ctx, "r1"); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM storage_run_usage`); got != 0 {
		t.Fatalf("per-run usage rows after the run was deleted = %d, want 0", got)
	}
	usage, err := st.StorageUsageFor(ctx, "alice", "", month)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.MonthBytes != 32 {
		t.Fatalf("the month total reads %d, want 32; a deleted run must not refund the month", usage.MonthBytes)
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
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "alice", MaxBytesPerRun: 1024,
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
