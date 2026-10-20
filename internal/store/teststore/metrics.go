package teststore

import (
	"context"
	"database/sql"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// SeedMetric inserts one SQLite sample for reporting projection fixtures.
func SeedMetric(db *sql.DB, ctx context.Context, run, job string, sample store.MetricSample) error {
	_, err := db.ExecContext(ctx, `INSERT INTO node_metrics(team,run_id,node_id,ts,cpu_millicores,memory_bytes,cpu_time_nanos,kind) VALUES (?,?,?,?,?,?,?,?)`, store.DefaultTeam, run, job, sample.TS.UnixNano(), sample.CPUMillicores, sample.MemoryBytes, int64(sample.CPUTime), sample.Kind)
	return err
}
