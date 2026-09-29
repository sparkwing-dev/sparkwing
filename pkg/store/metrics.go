package store

import (
	"context"
	"errors"
	"fmt"
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

// AddNodeMetricSample accepts identical retries and rejects conflicting readings
// at the same node timestamp.
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
	result, err := tx.ExecContext(ctx, `
INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos, kind)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (run_id, node_id, ts) DO NOTHING`,
		runID, nodeID, sample.TS.UnixNano(), sample.CPUMillicores, sample.MemoryBytes,
		int64(sample.CPUTime), sample.Kind)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		var cpu, memory, cpuTime int64
		var kind MetricKind
		if err := tx.QueryRowContext(ctx, `
SELECT cpu_millicores, memory_bytes, cpu_time_nanos, kind
  FROM node_metrics WHERE run_id = ? AND node_id = ? AND ts = ?`,
			runID, nodeID, sample.TS.UnixNano()).Scan(&cpu, &memory, &cpuTime, &kind); err != nil {
			return err
		}
		if cpu != sample.CPUMillicores || memory != sample.MemoryBytes || cpuTime != int64(sample.CPUTime) || kind != sample.Kind {
			return fmt.Errorf("conflicting node metric for %s/%s at %s", runID, nodeID, sample.TS.Format(time.RFC3339Nano))
		}
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

func invalidateNodeMeasurementHistory(ctx context.Context, tx *storeTx) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos, kind)
SELECT n.run_id, n.node_id,
       COALESCE((SELECT MIN(m.ts) FROM node_metrics m WHERE m.run_id = n.run_id AND m.node_id = n.node_id),
                n.started_at, n.finished_at, r.started_at),
       0, 0, 0, ''
  FROM nodes n JOIN runs r ON r.id = n.run_id
 WHERE NOT EXISTS (SELECT 1 FROM node_metrics m
                   WHERE m.run_id = n.run_id AND m.node_id = n.node_id AND m.kind = '')
ON CONFLICT (run_id, node_id, ts) DO UPDATE SET kind = ''`)
	return err
}
