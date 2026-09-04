package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func countNodeMetrics(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM node_metrics`).Scan(&n); err != nil {
		t.Fatalf("count node_metrics: %v", err)
	}
	return n
}

func seedRunWithMetricSample(t *testing.T, st *store.Store, runID string, startedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "success", StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("CreateRun %s: %v", runID, err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: "build", Status: "success",
	}); err != nil {
		t.Fatalf("CreateNode %s: %v", runID, err)
	}
	if err := st.AddNodeMetricSample(ctx, runID, "build", store.MetricSample{
		TS: startedAt, CPUMillicores: 500, MemoryBytes: 1 << 20,
	}); err != nil {
		t.Fatalf("AddNodeMetricSample %s: %v", runID, err)
	}
}

func TestDeleteRunRemovesItsNodeMetrics(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	seedRunWithMetricSample(t, st, "kept", time.Now())
	seedRunWithMetricSample(t, st, "deleted", time.Now())
	if got := countNodeMetrics(t, st.DB()); got != 2 {
		t.Fatalf("seeded node_metrics = %d, want 2", got)
	}

	if err := st.DeleteRun(ctx, "deleted"); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if got := countNodeMetrics(t, st.DB()); got != 1 {
		t.Fatalf("node_metrics after DeleteRun = %d, want 1", got)
	}
	samples, err := st.ListNodeMetrics(ctx, "deleted", "build")
	if err != nil {
		t.Fatalf("ListNodeMetrics: %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("ListNodeMetrics for a deleted run returned %d sample(s)", len(samples))
	}
	if kept, err := st.ListNodeMetrics(ctx, "kept", "build"); err != nil || len(kept) != 1 {
		t.Errorf("surviving run lost its samples: %d, %v", len(kept), err)
	}
}

func TestPruneRunsOlderThanRemovesNodeMetrics(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	seedRunWithMetricSample(t, st, "old", time.Now().Add(-48*time.Hour))
	seedRunWithMetricSample(t, st, "recent", time.Now())

	ids, err := st.PruneRunsOlderThan(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneRunsOlderThan: %v", err)
	}
	if len(ids) != 1 || ids[0] != "old" {
		t.Fatalf("pruned ids = %v, want [old]", ids)
	}
	if got := countNodeMetrics(t, st.DB()); got != 1 {
		t.Fatalf("node_metrics after prune = %d, want 1", got)
	}
}

const nodeMetricsV27Shape = `
CREATE TABLE node_metrics (
    run_id          TEXT NOT NULL,
    node_id         TEXT NOT NULL,
    ts              INTEGER NOT NULL,
    cpu_millicores  INTEGER NOT NULL,
    memory_bytes    INTEGER NOT NULL,
    cpu_time_nanos  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, node_id, ts)
);
CREATE INDEX IF NOT EXISTS idx_node_metrics_lookup
    ON node_metrics(run_id, node_id, ts);`

func downgradeNodeMetricsToV27(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open for downgrade: %v", err)
	}
	seedRunWithMetricSample(t, st, "live", time.Now())
	if _, err := st.DB().Exec(`DROP TABLE node_metrics`); err != nil {
		t.Fatalf("drop node_metrics: %v", err)
	}
	if _, err := st.DB().Exec(nodeMetricsV27Shape); err != nil {
		t.Fatalf("recreate v27 node_metrics: %v", err)
	}
	if _, err := st.DB().Exec(
		`INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos)
		 VALUES ('live', 'build', 1, 1, 1, 0), ('gone', 'build', 1, 1, 1, 0)`); err != nil {
		t.Fatalf("seed v27 samples: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 28`); err != nil {
		t.Fatalf("reset version to 27: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	if v := readSchemaVersion(t, st.DB()); v != 27 {
		t.Fatalf("seeded version = %d, want 27", v)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close after downgrade: %v", err)
	}
}

func TestSchemaV28_UpgradeOfARealV27ShapeCascadesAndDropsOrphans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "real27.db")
	downgradeNodeMetricsToV27(t, path)

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	defer func() { _ = up.Close() }()
	ctx := context.Background()

	if v := readSchemaVersion(t, up.DB()); v != store.ExpectedSchemaVersion() {
		t.Errorf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	assertExecutionPolicySchemaSQLite(t, up.DB())
	if got := countNodeMetrics(t, up.DB()); got != 1 {
		t.Fatalf("node_metrics after upgrade = %d, want 1 (the orphan should be gone)", got)
	}
	if samples, err := up.ListNodeMetrics(ctx, "live", "build"); err != nil || len(samples) != 1 {
		t.Fatalf("upgrade lost the live run's sample: %d, %v", len(samples), err)
	}
	if err := up.DeleteRun(ctx, "live"); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if got := countNodeMetrics(t, up.DB()); got != 0 {
		t.Fatalf("node_metrics after DeleteRun = %d, want 0", got)
	}
}

func countNodeMetricsRunForeignKeys(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_foreign_key_list('node_metrics') WHERE "table" = 'runs'`,
	).Scan(&n); err != nil {
		t.Fatalf("read node_metrics foreign keys: %v", err)
	}
	return n
}

func countIndexesNamed(t *testing.T, db *sql.DB, name string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatalf("read index %s: %v", name, err)
	}
	return n
}

func resetSchemaVersionTo27(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 28`); err != nil {
		t.Fatalf("reset version to 27: %v", err)
	}
	deleteFleetRequirements(t, db)
}

func TestSchemaV28_UpgradeIsSafeToReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay28.db")
	downgradeNodeMetricsToV27(t, path)

	for attempt := 1; attempt <= 3; attempt++ {
		st, err := store.Open(path)
		if err != nil {
			t.Fatalf("Open attempt %d: %v", attempt, err)
		}
		if got := countNodeMetrics(t, st.DB()); got != 1 {
			t.Fatalf("attempt %d: node_metrics = %d, want 1", attempt, got)
		}
		if got := countNodeMetricsRunForeignKeys(t, st.DB()); got != 1 {
			t.Fatalf("attempt %d: node_metrics foreign keys to runs = %d, want 1", attempt, got)
		}
		if got := countIndexesNamed(t, st.DB(), "idx_node_metrics_lookup"); got != 1 {
			t.Errorf("attempt %d: idx_node_metrics_lookup count = %d, want 1", attempt, got)
		}
		if v := readSchemaVersion(t, st.DB()); v != store.ExpectedSchemaVersion() {
			t.Errorf("attempt %d: version = %d, want %d", attempt, v, store.ExpectedSchemaVersion())
		}
		if attempt < 3 {
			resetSchemaVersionTo27(t, st.DB())
		}
		if err := st.Close(); err != nil {
			t.Fatalf("Close attempt %d: %v", attempt, err)
		}
	}
}

func TestSchemaV28_IndexesConcurrencyCacheByOriginRun(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "fresh.db")
	st, err := store.Open(fresh)
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	if got := countIndexesNamed(t, st.DB(), "idx_concurrency_cache_origin_run"); got != 1 {
		t.Errorf("fresh database: idx_concurrency_cache_origin_run count = %d, want 1", got)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close fresh: %v", err)
	}

	upgraded := filepath.Join(t.TempDir(), "upgraded.db")
	downgradeNodeMetricsToV27(t, upgraded)
	pre, err := store.Open(upgraded)
	if err != nil {
		t.Fatalf("Open for index drop: %v", err)
	}
	if _, err := pre.DB().Exec(`DROP INDEX IF EXISTS idx_concurrency_cache_origin_run`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	resetSchemaVersionTo27(t, pre.DB())
	if err := pre.Close(); err != nil {
		t.Fatalf("Close before upgrade: %v", err)
	}

	up, err := store.Open(upgraded)
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	defer func() { _ = up.Close() }()
	if got := countIndexesNamed(t, up.DB(), "idx_concurrency_cache_origin_run"); got != 1 {
		t.Errorf("after upgrade: idx_concurrency_cache_origin_run count = %d, want 1", got)
	}
}
