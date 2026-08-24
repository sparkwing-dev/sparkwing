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

	// CPUTime is the CPU a one-shot sample measured, set only by a
	// per-command report: the command's reaped subtree burned exactly this
	// much, over a span that is the command's own rather than the sampling
	// window the sample lands in. A sampler tick leaves it zero, because a
	// tick's rate already covers its whole window.
	//
	// A reader that groups samples by window sums rates for ticks and
	// integrals for one-shots. Summing one-shot rates instead reports
	// concurrency that never happened when commands ran back to back: four
	// 400ms commands at two cores inside one two-second window are 1.6
	// cores of draw, not eight.
	CPUTime time.Duration
}

// OneShot reports whether this sample is a per-command report rather than
// a sampler tick -- the distinction a window-grouping reader has to make,
// named once so every reader asks it the same way.
func (m MetricSample) OneShot() bool { return m.CPUTime > 0 }

// AddNodeMetricSample appends; duplicates by (run, node, ts) are
// silently ignored so retries don't trip UNIQUE.
func (s *Store) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample MetricSample) error {
	_, err := s.exec(ctx, `
INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (run_id, node_id, ts) DO NOTHING`,
		runID, nodeID, sample.TS.UnixNano(), sample.CPUMillicores, sample.MemoryBytes,
		max(int64(sample.CPUTime), 0))
	return err
}

// ListNodeMetrics returns samples oldest-first.
func (s *Store) ListNodeMetrics(ctx context.Context, runID, nodeID string) ([]MetricSample, error) {
	rows, err := s.query(ctx, `
SELECT ts, cpu_millicores, memory_bytes, cpu_time_nanos
  FROM node_metrics
 WHERE run_id = ? AND node_id = ?
 ORDER BY ts ASC`, runID, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []MetricSample{}
	for rows.Next() {
		var tsNs, cpu, mem, cpuTimeNs int64
		if err := rows.Scan(&tsNs, &cpu, &mem, &cpuTimeNs); err != nil {
			return nil, err
		}
		out = append(out, MetricSample{
			TS:            time.Unix(0, tsNs),
			CPUMillicores: cpu,
			MemoryBytes:   mem,
			CPUTime:       time.Duration(cpuTimeNs),
		})
	}
	return out, rows.Err()
}
