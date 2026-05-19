package store

import (
	"context"
	"time"
)

// MetricSample is one resource point.
type MetricSample struct {
	TS            time.Time
	CPUMillicores int64
	MemoryBytes   int64
}

// AddNodeMetricSample appends; duplicates by (run, node, ts) are
// silently ignored so retries don't trip UNIQUE.
func (s *Store) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample MetricSample) error {
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes)
VALUES (?, ?, ?, ?, ?)`,
		runID, nodeID, sample.TS.UnixNano(), sample.CPUMillicores, sample.MemoryBytes)
	return err
}

// ListNodeMetrics returns samples oldest-first.
func (s *Store) ListNodeMetrics(ctx context.Context, runID, nodeID string) ([]MetricSample, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT ts, cpu_millicores, memory_bytes
  FROM node_metrics
 WHERE run_id = ? AND node_id = ?
 ORDER BY ts ASC`, runID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MetricSample{}
	for rows.Next() {
		var tsNs, cpu, mem int64
		if err := rows.Scan(&tsNs, &cpu, &mem); err != nil {
			return nil, err
		}
		out = append(out, MetricSample{
			TS:            time.Unix(0, tsNs),
			CPUMillicores: cpu,
			MemoryBytes:   mem,
		})
	}
	return out, rows.Err()
}
