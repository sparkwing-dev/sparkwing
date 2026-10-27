package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func (o *Operator) maintainRetention(ctx context.Context, request RetentionRequest) (RetentionResult, error) {
	var result RetentionResult
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		result, err = tx.operator.maintainRetentionTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return RetentionResult{}, err
	}
	return result, nil
}

func (op *Operator) maintainRetentionTx(ctx context.Context, tx *storeTx, request RetentionRequest) (RetentionResult, error) {
	if request.Limit <= 0 {
		return RetentionResult{}, fmt.Errorf("retention limit must be positive")
	}

	now := tx.now(ctx)
	runDays, err := numericSettingTx(ctx, tx, "operator.run_retention_days", 0)
	if err != nil {
		return RetentionResult{}, err
	}
	metricDays, err := numericSettingTx(ctx, tx, "operator.node_metric_retention_days", 0)
	if err != nil {
		return RetentionResult{}, err
	}
	var result RetentionResult
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pipeline_success_pins`).Scan(&result.PinnedRuns); err != nil {
		return RetentionResult{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM blob_records b JOIN pipeline_success_pins p ON p.team = b.team AND p.run_id = b.run_id WHERE b.kind = 'output'`).Scan(&result.PinnedOutputs); err != nil {
		return RetentionResult{}, err
	}
	if metricDays > 0 {
		cutoff, err := retentionBoundary(now, metricDays)
		if err != nil {
			return RetentionResult{}, err
		}
		result.Metrics, err = tx.operator.deleteRetentionMetricsTx(ctx, tx, cutoff, request.Limit)
		if err != nil {
			return RetentionResult{}, err
		}
	}
	if runDays > 0 {
		cutoff, err := retentionBoundary(now, runDays)
		if err != nil {
			return RetentionResult{}, err
		}
		result.Runs, result.ReleasedBytes, err = tx.operator.deleteRetentionRunsTx(ctx, tx, cutoff, request.Limit)
		if err != nil {
			return RetentionResult{}, err
		}
	}
	result.Records, err = tx.operator.deleteRetentionRecordsTx(ctx, tx, now, request.Limit)
	if err != nil {
		return RetentionResult{}, err
	}
	result.Capabilities, err = tx.operator.deleteRetentionCapabilitiesTx(ctx, tx, now.Add(-7*24*time.Hour), request.Limit)
	if err != nil {
		return RetentionResult{}, err
	}
	result.Effects, err = tx.operator.deleteRetentionEffectsTx(ctx, tx, now.Add(-7*24*time.Hour), request.Limit)
	if err != nil {
		return RetentionResult{}, err
	}
	res, err := tx.execContext(ctx, `DELETE FROM audit_events WHERE id IN
(SELECT id FROM audit_events WHERE recorded_at < `+auditRetentionCutoffSQL(tx.dialect)+` ORDER BY recorded_at,id LIMIT ?)`, request.Limit)
	if err != nil {
		return RetentionResult{}, err
	}
	result.AuditEvents, err = res.RowsAffected()
	if err != nil {
		return RetentionResult{}, err
	}
	if err := tx.operator.deleteRetentionIdentityTx(ctx, tx, now, request.Limit, &result); err != nil {
		return RetentionResult{}, err
	}
	result.LogSlots, err = tx.operator.deleteExpiredLogSlotsTx(ctx, tx, now)
	if err != nil {
		return RetentionResult{}, err
	}
	return result, nil
}

// deleteExpiredLogSlotsTx stops counting the log objects of streams older than their retention class, which the
// object store's lifecycle (or filesystem expiry) deletes outside Sparkwing, so retained bytes fall with them.
func (op *Operator) deleteExpiredLogSlotsTx(ctx context.Context, tx *storeTx, now time.Time) (int64, error) {
	rows, err := tx.queryContext(ctx, `SELECT DISTINCT s.retention_class FROM log_streams s WHERE EXISTS(SELECT 1 FROM log_slots l WHERE l.team=s.team AND l.run_id=s.run_id AND l.stream_id=s.id) ORDER BY s.retention_class`)
	if err != nil {
		return 0, err
	}
	var classes []string
	for rows.Next() {
		var class string
		if err := rows.Scan(&class); err != nil {
			return 0, errors.Join(err, rows.Close())
		}
		classes = append(classes, class)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	var removed int64
	for _, class := range classes {
		days, ok := blob.LogClassDays(class)
		if !ok {
			return removed, fmt.Errorf("storage: invalid persisted log retention class %q", class)
		}
		cutoff, err := retentionBoundary(now, int64(days))
		if err != nil {
			return removed, err
		}
		res, err := tx.execContext(ctx, `DELETE FROM log_slots WHERE EXISTS(SELECT 1 FROM log_streams s WHERE s.team=log_slots.team AND s.run_id=log_slots.run_id AND s.id=log_slots.stream_id AND s.retention_class=? AND s.created_at<=?)`, class, cutoff.UnixNano())
		if err != nil {
			return removed, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

const maxRetentionDays = math.MaxInt64 / int64(24*time.Hour)

func retentionBoundary(now time.Time, days int64) (time.Time, error) {
	if days > maxRetentionDays {
		return time.Time{}, fmt.Errorf("retention duration overflows")
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour), nil
}

type retentionRun struct {
	team  Team
	id    string
	bytes int64
}

func (op *Operator) deleteRetentionRunsTx(ctx context.Context, tx *storeTx, cutoff time.Time, limit int) (int64, int64, error) {
	rows, err := tx.queryContext(ctx, `SELECT r.team,r.id,r.event_bytes FROM runs r WHERE r.finished_at < ?
AND NOT EXISTS (SELECT 1 FROM pipeline_success_pins p WHERE p.team = r.team AND p.run_id = r.id)
AND NOT EXISTS (SELECT 1 FROM run_effects e WHERE e.team = r.team AND e.run_id = r.id AND e.delivered_rev < e.desired_rev)
ORDER BY r.finished_at,r.team,r.id LIMIT ?`, cutoff.UnixNano(), limit)
	if err != nil {
		return 0, 0, err
	}
	var runs []retentionRun
	for rows.Next() {
		var run retentionRun
		if err := rows.Scan(&run.team, &run.id, &run.bytes); err != nil {
			return 0, 0, errors.Join(err, rows.Close())
		}
		runs = append(runs, run)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, 0, err
	}
	var count, released int64
	var teams []Team
	for _, run := range runs {
		res, err := tx.execContext(ctx, `DELETE FROM runs WHERE team = ? AND id = ? AND finished_at < ? AND NOT EXISTS (SELECT 1 FROM pipeline_success_pins p WHERE p.team = runs.team AND p.run_id = runs.id) AND NOT EXISTS (SELECT 1 FROM run_effects e WHERE e.team = runs.team AND e.run_id = runs.id AND e.delivered_rev < e.desired_rev)`, run.team, run.id, cutoff.UnixNano())
		if err != nil {
			return 0, 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
		count += n
		released += n * run.bytes
		if n > 0 && !slices.Contains(teams, run.team) {
			teams = append(teams, run.team)
		}
	}
	slices.Sort(teams)
	for _, team := range teams {
		if _, err := tx.execContext(ctx, `DELETE FROM claim_contexts WHERE team=? AND id NOT IN (SELECT claim_context FROM nodes WHERE team=? UNION SELECT claim_context FROM triggers WHERE team=?)`, team, team, team); err != nil {
			return 0, 0, err
		}
		if _, err := tx.execContext(ctx, `DELETE FROM claim_requirements WHERE team=? AND id NOT IN (SELECT claim_requirements FROM nodes WHERE team=? UNION SELECT claim_requirements FROM triggers WHERE team=?)`, team, team, team); err != nil {
			return 0, 0, err
		}
	}
	return count, released, nil
}

func (op *Operator) deleteRetentionMetricsTx(ctx context.Context, tx *storeTx, cutoff time.Time, limit int) (int64, error) {
	res, err := tx.execContext(ctx, `DELETE FROM node_metrics WHERE (team,run_id,node_id,ts) IN
(SELECT team,run_id,node_id,ts FROM node_metrics WHERE ts < ? ORDER BY ts,team,run_id,node_id LIMIT ?)`, cutoff.UnixNano(), limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (op *Operator) deleteRetentionRecordsTx(ctx context.Context, tx *storeTx, now time.Time, limit int) (int64, error) {
	res, err := tx.execContext(ctx, `DELETE FROM blob_records WHERE (team,kind,scope,name) IN
(SELECT b.team,b.kind,b.scope,b.name FROM blob_records b WHERE
((b.expires_at IS NOT NULL AND b.expires_at <= ?) OR (b.kind = 'output' AND b.created_at < ?))
AND NOT EXISTS (SELECT 1 FROM pipeline_success_pins p WHERE p.team = b.team AND p.run_id = b.run_id AND b.kind = 'output')
ORDER BY b.created_at,b.team,b.kind,b.scope,b.name LIMIT ?)`, now.UnixNano(), now.Add(-30*24*time.Hour).UnixNano(), limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (op *Operator) deleteRetentionCapabilitiesTx(ctx context.Context, tx *storeTx, cutoff time.Time, limit int) (int64, error) {
	res, err := tx.execContext(ctx, `DELETE FROM capabilities WHERE prefix IN (SELECT c.prefix FROM capabilities c
WHERE ((c.expires_at IS NOT NULL AND c.expires_at <= ?) OR (c.revoked_at IS NOT NULL AND c.revoked_at <= ?))
AND NOT EXISTS (SELECT 1 FROM capabilities child WHERE child.parent_kind = 'capability' AND child.parent_id = c.prefix)
ORDER BY c.prefix LIMIT ?)`, cutoff.UnixNano(), cutoff.UnixNano(), limit)
	if err != nil {
		return 0, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (op *Operator) deleteRetentionEffectsTx(ctx context.Context, tx *storeTx, cutoff time.Time, limit int) (int64, error) {
	res, err := tx.execContext(ctx, `DELETE FROM run_effects WHERE (team,run_id,effect) IN
(SELECT e.team,e.run_id,e.effect FROM run_effects e JOIN runs r ON r.team = e.team AND r.id = e.run_id
WHERE r.finished_at IS NOT NULL AND e.delivered_rev = e.desired_rev AND e.delivered_at <= ?
ORDER BY e.delivered_at,e.team,e.run_id,e.effect LIMIT ?)`, cutoff.UnixNano(), limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
