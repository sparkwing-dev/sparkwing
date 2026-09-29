package store

import (
	"context"
	"fmt"
	"time"
)

type MetricKind string

const (
	MetricInterval MetricKind = "interval"
	MetricCommand  MetricKind = "command"
	MetricEstimate MetricKind = "estimate"
)

// MetricSample keeps command lifetime usage separate from sampled intervals.
// Availability describes the named quantity, not complete workload coverage.
type MetricSample struct {
	TS              time.Time
	Kind            MetricKind
	CPUAvailable    bool
	MemoryAvailable bool
	CPUMillicores   int64
	MemoryBytes     int64
	CPUTime         time.Duration
}

func (m MetricSample) OneShot() bool { return m.Kind == MetricCommand }

func (m MetricSample) Validate() error {
	switch m.Kind {
	case "":
		if m.CPUAvailable || m.MemoryAvailable {
			return fmt.Errorf("unknown metric kind cannot declare measurement availability")
		}
	case MetricInterval, MetricEstimate:
		if m.CPUTime != 0 {
			return fmt.Errorf("interval metric cannot carry command CPU time")
		}
	case MetricCommand:
	default:
		return fmt.Errorf("unknown metric kind %q", m.Kind)
	}
	if m.CPUMillicores < 0 || m.MemoryBytes < 0 || m.CPUTime < 0 {
		return fmt.Errorf("metric quantities must be nonnegative")
	}
	return nil
}

// AddNodeMetricSample appends; duplicates by (run, node, ts) are
// silently ignored so retries don't trip UNIQUE.
func (s *Store) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample MetricSample) error {
	if err := sample.Validate(); err != nil {
		return err
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
INSERT INTO node_metrics (run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos, sample_kind, cpu_available, memory_available)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (run_id, node_id, ts) DO NOTHING`,
		runID, nodeID, sample.TS.UnixNano(), sample.CPUMillicores, sample.MemoryBytes,
		int64(sample.CPUTime), sample.Kind, boolToInt(sample.CPUAvailable), boolToInt(sample.MemoryAvailable)); err != nil {
		return err
	}
	return tx.Commit()
}

// ListNodeMetrics returns samples oldest-first.
func (s *Store) ListNodeMetrics(ctx context.Context, runID, nodeID string) ([]MetricSample, error) {
	rows, err := s.query(ctx, `
SELECT ts, cpu_millicores, memory_bytes, cpu_time_nanos, sample_kind, cpu_available, memory_available
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
		var cpuAvailable, memoryAvailable int
		if err := rows.Scan(&tsNs, &cpu, &mem, &cpuTimeNs, &kind, &cpuAvailable, &memoryAvailable); err != nil {
			return nil, err
		}
		out = append(out, MetricSample{
			TS:   time.Unix(0, tsNs),
			Kind: kind, CPUAvailable: cpuAvailable != 0, MemoryAvailable: memoryAvailable != 0,
			CPUMillicores: cpu,
			MemoryBytes:   mem,
			CPUTime:       time.Duration(cpuTimeNs),
		})
	}
	return out, rows.Err()
}
