package store

import (
	"context"
	"errors"
	"time"
)

// MetricKind identifies the interval over which a resource reading was collected.
type MetricKind string

const (
	MetricUnknown  MetricKind = ""
	MetricInterval MetricKind = "interval"
	MetricCommand  MetricKind = "command"
)

// MetricSample is one resource point.
type MetricSample struct {
	Kind          MetricKind
	TS            time.Time
	CPUMillicores int64
	MemoryBytes   int64

	// CPUTime covers a command's lifetime, not the sampling interval at its
	// completion timestamp. Interval readings leave it zero.
	CPUTime time.Duration
}

// OneShot reports whether this reading covers a completed command.
func (m MetricSample) OneShot() bool { return m.Kind == MetricCommand }

// AddNodeMetricSample appends; duplicates by (run, node, ts) are
// silently ignored so retries don't trip UNIQUE.
func (s *Store) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample MetricSample) error {
	if sample.Kind != MetricUnknown && sample.Kind != MetricInterval && sample.Kind != MetricCommand {
		return errors.New("invalid node metric kind")
	}
	if sample.Kind == MetricInterval && sample.CPUTime != 0 {
		return errors.New("interval metrics cannot contain command CPU time")
	}
	if sample.CPUMillicores < 0 || sample.MemoryBytes < 0 || sample.CPUTime < 0 {
		return errors.New("node metrics require nonnegative CPU rate, memory and CPU time")
	}
	if !time.Unix(0, sample.TS.UnixNano()).Equal(sample.TS) {
		return errors.New("node metric timestamp exceeds the nanosecond storage range")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.assertNodeMutationFenceTx(ctx, tx, runID, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos, kind)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (run_id, node_id, ts) DO NOTHING`,
		runID, nodeID, sample.TS.UnixNano(), sample.CPUMillicores, sample.MemoryBytes,
		int64(sample.CPUTime), sample.Kind); err != nil {
		return err
	}
	return tx.Commit()
}

// ListNodeMetrics returns samples oldest-first.
func (s *Store) ListNodeMetrics(ctx context.Context, runID, nodeID string) ([]MetricSample, error) {
	rows, err := s.query(ctx, `
SELECT ts, cpu_millicores, memory_bytes, cpu_time_nanos, kind
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
		var kind MetricKind
		if err := rows.Scan(&tsNs, &cpu, &mem, &cpuTimeNs, &kind); err != nil {
			return nil, err
		}
		out = append(out, MetricSample{
			Kind:          kind,
			TS:            time.Unix(0, tsNs),
			CPUMillicores: cpu,
			MemoryBytes:   mem,
			CPUTime:       time.Duration(cpuTimeNs),
		})
	}
	return out, rows.Err()
}
