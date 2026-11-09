package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func BenchmarkInsertMetric(b *testing.B) {
	for _, held := range []int{1000, 9000, 9999} {
		b.Run(fmt.Sprint(held), func(b *testing.B) {
			db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(b.TempDir(), "metrics.db")))
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			ctx := b.Context()
			_, err = db.ExecContext(ctx, `CREATE TABLE node_metrics (
team TEXT NOT NULL, run_id TEXT NOT NULL, node_id TEXT NOT NULL, ts BIGINT NOT NULL,
cpu_millicores BIGINT NOT NULL, memory_bytes BIGINT NOT NULL,
cpu_time_nanos BIGINT NOT NULL DEFAULT 0, kind TEXT NOT NULL DEFAULT '',
PRIMARY KEY (team, run_id, node_id, ts))`)
			if err != nil {
				b.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			for node := range 10 {
				n := 10000
				if node == 0 {
					n = held
				}
				for i := range n {
					if _, err := tx.ExecContext(ctx, `INSERT INTO node_metrics VALUES ('bench','run',?,?,1,1,0,'interval')`, fmt.Sprint(node), i); err != nil {
						b.Fatal(err)
					}
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			s := &Store{}
			sample := MetricSample{Kind: MetricInterval, TS: time.Unix(0, 10001), CPUMillicores: 1, MemoryBytes: 1}
			ref := runspec.NodeRef{RunID: "run", NodeID: "0"}
			b.ResetTimer()
			for range b.N {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					b.Fatal(err)
				}
				wrapped := &storeTx{tx: tx, dialect: DialectSQLite, store: s}
				if err := insertMetricTx(ctx, wrapped, "bench", ref, sample); err != nil {
					b.Fatal(err)
				}
				if err := tx.Rollback(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
