package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

// MaxNodeMetricSamples bounds one node's stored resource samples: at the
// sampler's two-second tick, about five and a half hours of a node.
const MaxNodeMetricSamples = limits.MaxMetricSamples

// MaxNodeMetricMarkers is how many marker samples a node may hold past
// [MaxNodeMetricSamples], so a measurement that filled the cap can still be
// labeled partial or excluded.
const MaxNodeMetricMarkers = 8

// ErrNodeMetricLimit refuses a sample past [MaxNodeMetricSamples].
var ErrNodeMetricLimit = fmt.Errorf("store: a node holds at most %d metric samples", limits.MaxMetricSamples)

// EventKindMetricsStopped records that a node's sampler stopped at
// [MaxNodeMetricSamples], so its resource charts end there.
const EventKindMetricsStopped = "metrics_stopped"

// MetricKind identifies the interval over which a resource reading was collected.
type MetricKind string

const (
	MetricUnknown  MetricKind = ""
	MetricInterval MetricKind = "interval"
	MetricCommand  MetricKind = "command"
	MetricPartial  MetricKind = "partial"
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

// Marker reports whether the sample only labels its node's measurement,
// carrying no resource reading.
func (sample MetricSample) Marker() bool {
	return (sample.Kind == MetricUnknown || sample.Kind == MetricPartial) && sample.CPUMillicores == 0 && sample.MemoryBytes == 0 && sample.CPUTime == 0
}

// ValidateResourceValues rejects invalid kinds, negative resource values,
// and command CPU time in an interval.
func (sample MetricSample) ValidateResourceValues() error {
	if sample.Kind != MetricUnknown && sample.Kind != MetricInterval && sample.Kind != MetricCommand && sample.Kind != MetricPartial {
		return errors.New("invalid node metric kind")
	}
	if sample.Kind == MetricInterval && sample.CPUTime != 0 {
		return errors.New("interval metrics cannot contain command CPU time")
	}
	if sample.CPUMillicores < 0 || sample.MemoryBytes < 0 || sample.CPUTime < 0 {
		return errors.New("node metrics require nonnegative CPU rate, memory and CPU time")
	}
	return nil
}

func insertMetricTx(ctx context.Context, tx *storeTx, team Team, ref runspec.NodeRef, sample MetricSample) error {
	runID, nodeID := ref.RunID, ref.NodeID
	result, err := tx.ExecContext(ctx, `
INSERT INTO node_metrics (team, run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos, kind)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (team, run_id, node_id, ts) DO NOTHING`,
		team, runID, nodeID, sample.TS.UnixNano(), sample.CPUMillicores, sample.MemoryBytes,
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
  FROM node_metrics WHERE team = ? AND run_id = ? AND node_id = ? AND ts = ?`,
			team, runID, nodeID, sample.TS.UnixNano()).Scan(&cpu, &memory, &cpuTime, &kind); err != nil {
			return err
		}
		if cpu != sample.CPUMillicores || memory != sample.MemoryBytes || cpuTime != int64(sample.CPUTime) || kind != sample.Kind {
			return fmt.Errorf("conflicting node metric for %s/%s at %s", runID, nodeID, sample.TS.Format(time.RFC3339Nano))
		}
	}
	if inserted == 1 {
		var held int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_metrics WHERE team=? AND run_id=? AND node_id=?`, team, runID, nodeID).Scan(&held); err != nil {
			return err
		}
		if held > MaxNodeMetricSamples && (!sample.Marker() || held > MaxNodeMetricSamples+MaxNodeMetricMarkers) {
			return ErrNodeMetricLimit
		}
	}

	return nil
}

// ListNodeMetrics returns every sample of a node of one of t's runs,
// oldest-first.
func (t *Tenant) ListNodeMetrics(ctx context.Context, runID, nodeID string) ([]MetricSample, error) {
	return t.ListNodeMetricsPage(ctx, runID, nodeID, time.Time{}, 0)
}

// ListNodeMetricsPage returns a tenant job's metric samples after the cursor.
func (t *Tenant) ListNodeMetricsPage(ctx context.Context, runID, nodeID string, after time.Time, limit int) ([]MetricSample, error) {
	query := `
SELECT ts, cpu_millicores, memory_bytes, cpu_time_nanos, kind
  FROM node_metrics
 WHERE team = ? AND run_id = ? AND node_id = ?`
	args := []any{string(t.team), runID, nodeID}
	// safety: a sample may sit at the lowest storable timestamp, so the first
	// page carries no lower bound rather than one it could equal.
	if !after.IsZero() {
		query += ` AND ts > ?`
		args = append(args, after.UnixNano())
	}
	query += ` ORDER BY ts ASC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := t.s.query(ctx, query, args...)
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
