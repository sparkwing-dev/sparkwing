package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

// safety: unit leases combine authority, funding and durable stop deadlines here;
// capacity writers use leaseBound with their unit lease and stop deadline too.
func leaseBoundTx(ctx context.Context, tx *storeTx, prefix string, now time.Time, funded ...time.Time) (time.Time, error) {
	c, err := loadCapabilityTx(ctx, tx, prefix)
	if err != nil {
		return time.Time{}, err
	}
	drain, err := cancelDrainTx(ctx, tx, Team(c.Team), c.Binding.RunID, c.Binding.NodeID)
	if err != nil {
		return time.Time{}, err
	}
	return leaseBound(now, append(funded, drain, deref(c.ExpiresAt), deref(c.Binding.Deadline), deref(c.RevokedAt))...)
}

// safety: an absent bound leaves the default lease, and expired authority cannot be renewed.
func leaseBound(now time.Time, bounds ...time.Time) (time.Time, error) {
	expiry := now.Add(limits.DefaultLease)
	for _, bound := range bounds {
		if !bound.IsZero() && bound.Before(expiry) {
			expiry = bound
		}
	}
	if !expiry.After(now) {
		return time.Time{}, holderRefusal()
	}
	return expiry, nil
}

// safety: a funded award is bounded by when its paid seconds run out if it executes from now on, counting the time it
// ran rather than the billing minimum the payment also covers. A refusal can shorten that horizon to its durable
// drain deadline. Work that is not metered has no funded bound.
func fundedBoundTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, generation int64, now time.Time) (time.Time, error) {
	a, err := unitBillingAwardTx(ctx, tx, team, unit, generation)
	if err != nil || a == nil || !a.provenance.Metered {
		return time.Time{}, err
	}
	if a.settled.Valid {
		if a.drain.Valid {
			return time.Unix(0, a.drain.Int64).UTC(), nil
		}
		return time.Time{}, nil
	}
	intervals, err := readBillingIntervalsTx(ctx, tx, *a, now, false)
	if err != nil {
		return time.Time{}, err
	}
	_, paid, err := paidBillingTx(ctx, tx, *a)
	if err != nil {
		return time.Time{}, err
	}
	horizon := now.Add(time.Duration(paid) * time.Second)
	for _, interval := range intervals {
		horizon = horizon.Add(-interval.End.Sub(interval.Start))
	}
	if a.drain.Valid && a.drain.Int64 < horizon.UnixNano() {
		horizon = time.Unix(0, a.drain.Int64).UTC()
	}
	return horizon, nil
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

const cancelDrainSources = `SELECT drain_deadline_at AS deadline FROM runs WHERE team=? AND id=?
 UNION ALL SELECT drain_deadline_at FROM triggers WHERE team=? AND id=?
 UNION ALL SELECT drain_deadline_at FROM nodes WHERE team=? AND run_id=? AND node_id=`

func cancelDrainTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) (time.Time, error) {
	var deadline sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT MIN(deadline) FROM (`+cancelDrainSources+`?) drains`, team, runID, team, runID, team, runID, nodeID).Scan(&deadline)
	if err != nil || !deadline.Valid {
		return time.Time{}, err
	}
	return time.Unix(0, deadline.Int64).UTC(), nil
}

// Existing leases must obey cancellation even if the holder never renews again.
func clampRunLeasesTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) error {
	for _, table := range []string{"nodes", "triggers", "eval_units", "concurrency_holders", "concurrency_waiters"} {
		if nodeID != "" && table == "triggers" {
			continue
		}
		runColumn, nodeColumn := "run_id", "node_id"
		if table == "triggers" {
			runColumn, nodeColumn = "id", "''"
		}
		if table == "eval_units" {
			nodeColumn = "source_node_id"
		}
		if table != "triggers" {
			nodeColumn = "leased." + nodeColumn
		}
		_, err := tx.ExecContext(ctx, `UPDATE `+table+` AS leased SET lease_expires_at=(
 SELECT MIN(deadline) FROM (
 SELECT leased.lease_expires_at AS deadline
 UNION ALL `+cancelDrainSources+nodeColumn+`
 ) bounds) WHERE team=? AND `+runColumn+`=? AND lease_expires_at IS NOT NULL AND (?='' OR `+nodeColumn+`=?)`, team, runID, team, runID, team, runID, team, runID, nodeID, nodeID)
		if err != nil {
			return err
		}
	}
	return nil
}
