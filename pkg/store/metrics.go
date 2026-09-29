package store

import (
	"context"
	"errors"
	"time"
)

// MaxNodeMetricSamples bounds one node's stored resource samples: at the
// sampler's two-second tick, about five and a half hours of a node.
const MaxNodeMetricSamples = 10_000

// ErrNodeMetricLimit refuses a sample past [MaxNodeMetricSamples].
var ErrNodeMetricLimit = errors.New("store: a node holds at most 10000 metric samples")

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
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.assertNodeMutationFenceTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_metrics WHERE run_id = ? AND node_id = ?`,
		runID, nodeID).Scan(&held); err != nil {
		return err
	}
	if held >= MaxNodeMetricSamples {
		return ErrNodeMetricLimit
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_metrics (team, run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos)
VALUES (`+runTeamSQL+`, ?, ?, ?, ?, ?, ?)
ON CONFLICT (run_id, node_id, ts) DO NOTHING`,
		runID, runID, nodeID, sample.TS.UnixNano(), sample.CPUMillicores, sample.MemoryBytes,
		max(int64(sample.CPUTime), 0)); err != nil {
		return err
	}
	return tx.Commit()
}

// ListNodeMetrics returns every sample oldest-first.
func (s *Store) ListNodeMetrics(ctx context.Context, runID, nodeID string) ([]MetricSample, error) {
	return s.ListNodeMetricsPage(ctx, runID, nodeID, time.Time{}, 0)
}

// ListNodeMetricsPage returns up to limit samples oldest-first whose
// timestamps follow after; a zero after starts at the first sample and a
// limit of zero or less returns them all. A node holds at most one sample
// per timestamp, so the last sample's TS continues the listing.
func (s *Store) ListNodeMetricsPage(ctx context.Context, runID, nodeID string, after time.Time, limit int) ([]MetricSample, error) {
	query := `
SELECT ts, cpu_millicores, memory_bytes, cpu_time_nanos
  FROM node_metrics
 WHERE run_id = ? AND node_id = ? AND ts > ?
 ORDER BY ts ASC`
	args := []any{runID, nodeID, int64(-1 << 63)}
	if !after.IsZero() {
		args[2] = after.UnixNano()
	}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.query(ctx, query, args...)
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
