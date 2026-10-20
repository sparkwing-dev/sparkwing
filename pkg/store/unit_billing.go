package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func meteredPlacement(claimant, root capability.Capability) bool {
	return launcherClaimant(claimant) || root.RootMetered
}

func persistUnitBillingTx(ctx context.Context, tx *storeTx, c capability.Capability) error {
	if c.Purpose == capability.PurposeHere {
		return nil
	}
	var price CreditRate
	var reservedSeconds int64
	if c.Billing.Metered {
		run, err := readRunTx(ctx, tx, Team(c.Team), c.Binding.RunID)
		if err != nil {
			return err
		}
		resources, err := unitResourcesTx(ctx, tx, Team(c.Team), run, UnitRef{Kind: c.Unit, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID})
		if err != nil {
			return err
		}
		price, err = unitBillingPriceTx(ctx, tx, matchingResources(resources))
		if err != nil {
			return err
		}
		l, err := lockLedgerTx(ctx, tx, Team(c.Team))
		if err != nil {
			return err
		}
		if c.Billing.ExecutorClass == "cloud" {
			if err := checkOverflowTx(ctx, tx, Team(c.Team), c.Binding.RunID); err != nil {
				return err
			}
			if err := checkCloudCapacityTx(ctx, l, price.Cores); err != nil {
				return err
			}
		}
		reservedSeconds = SliceSeconds
	}
	return recordBillingAwardTx(ctx, tx, c, price, reservedSeconds)
}

func unitResourcesTx(ctx context.Context, tx *storeTx, team Team, run RunRow, unit UnitRef) (runspec.Resources, error) {
	if unit.Kind == capability.UnitNode {
		row, err := readNodeTx(ctx, tx, team, NodeRef{unit.RunID, unit.NodeID})
		return row.Execution.Resources.Value, err
	}
	if run.Spec != nil {
		return run.Spec.Resolved.Settings.Options.Resources.Value, nil
	}
	return run.Request.Request.Options.Resources.Value, nil
}

func billingUnitKind(kind capability.UnitKind) capability.BillingUnit {
	for _, mapping := range capability.UnitBillingMappings {
		if mapping.Operational == kind {
			return mapping.Billing
		}
	}
	return ""
}

func unitBillingPriceTx(ctx context.Context, tx *storeTx, resources match.Resources) (CreditRate, error) {
	table, err := creditRateTableTx(ctx, tx)
	if err != nil {
		return CreditRate{}, err
	}
	return table.ClassForResource(resources)
}

func eligibleUnitBillingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, price CreditRate, cloud bool) (bool, error) {
	var err error
	if cloud {
		err = checkOverflowTx(ctx, tx, team, unit.RunID)
		var overflow *apierr.Error
		if errors.As(err, &overflow) && overflow.Code == apierr.LockHeld {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		l, err := lockLedgerTx(ctx, tx, team)
		if err != nil {
			return false, err
		}
		err = checkCloudCapacityTx(ctx, l, price.Cores)
		var refused *apierr.Error
		if errors.As(err, &refused) && (refused.Code == apierr.BadRequest || refused.Code == apierr.LockHeld) || errors.Is(err, ErrInsufficientCredits) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	err = unitCreditRefusalTx(ctx, tx, team, unit, price)
	if errors.Is(err, ErrInsufficientCredits) {
		return false, nil
	}
	return err == nil, err
}

func unitCreditRefusalTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, price CreditRate) error {
	amount, err := billingProduct(SliceSeconds, price.MicroPerSecond)
	if err != nil {
		return err
	}
	l, err := lockLedgerTx(ctx, tx, team)
	if err != nil {
		return err
	}
	return refuseUnfundedTx(ctx, l, amount, unit.RunID, unit.NodeID)
}

func unitBillingAwardTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, generation int64) (*billingAward, error) {
	if generation <= 0 {
		return nil, nil
	}
	var reservation string
	err := tx.QueryRowContext(ctx, `SELECT reservation_id FROM billing_awards WHERE team=? AND run_id=? AND node_id=? AND unit_kind=? AND generation=?`, team, unit.RunID, unit.NodeID, unit.Kind, generation).Scan(&reservation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return nil, err
	}
	award, err := readBillingAwardTx(ctx, tx, team, reservation)
	return &award, err
}

func computeUnitBillingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, generation, attempt int64, executing bool) error {
	a, err := unitBillingAwardTx(ctx, tx, team, unit, generation)
	if err != nil || a == nil || !a.provenance.Metered {
		return err
	}
	if a.settled.Valid {
		return authorityError(apierr.Conflict, "billing award has ended")
	}
	var active string
	err = tx.QueryRowContext(ctx, `SELECT id FROM billable_intervals WHERE team=? AND reservation_id=? AND finished_at IS NULL`, team, a.provenance.ReservationID).Scan(&active)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if executing == (active != "") {
		return nil
	}
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM billable_intervals WHERE team=? AND reservation_id=?`, team, a.provenance.ReservationID).Scan(&count); err != nil {
		return err
	}
	if executing {
		return startBillableIntervalTx(ctx, tx, team, a.provenance.ReservationID, fmt.Sprintf("compute:%d", count), attempt, tx.now())
	}
	key := fmt.Sprintf("compute:%d", count-1)
	if active == billingID("interval", team, a.provenance.ReservationID, "award") {
		key = "award"
	}
	return finishBillableIntervalTx(ctx, tx, team, a.provenance.ReservationID, key, tx.now())
}

func settleUnitBillingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, generation int64, through time.Time) error {
	a, err := unitBillingAwardTx(ctx, tx, team, unit, generation)
	if err != nil || a == nil || a.settled.Valid {
		return err
	}
	_, err = accountBillingAwardTx(ctx, tx, team, a.provenance.ReservationID, through, true)
	return err
}

// safety: a renewal charges what the work used and pays for its next slice in one step. A balance or hold that cannot
// pay writes the award's durable stop, a drain deadline one slice out and the run's cancellation in the same
// transaction, so every later renewal reports the refusal and no lease outlives the drain. It returns the unit's
// funded bound for leaseBoundTx.
func renewUnitBillingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, generation int64, beat *LeaseBeat) (time.Time, error) {
	a, err := unitBillingAwardTx(ctx, tx, team, unit, generation)
	if err != nil || a == nil {
		return time.Time{}, err
	}
	if a.stop != "" {
		beat.CancelRequested, beat.CancelReason = true, a.stop
	}
	if a.settled.Valid {
		if a.provenance.Metered && !a.drain.Valid {
			return time.Time{}, holderRefusal()
		}
		return fundedBoundTx(ctx, tx, team, unit, generation, tx.now())
	}
	now := tx.now()
	err = renewBillingAwardTx(ctx, tx, team, a.provenance.ReservationID, now, a.provenance.Metered && !a.drain.Valid)
	if refused := (*InsufficientCreditsError)(nil); errors.As(err, &refused) {
		err = stopAwardTx(ctx, tx, team, *a, refused.Error(), now.Add(SliceSeconds*time.Second))
	}
	if err != nil {
		return time.Time{}, err
	}
	a, err = unitBillingAwardTx(ctx, tx, team, unit, generation)
	if err != nil || a == nil {
		return time.Time{}, err
	}
	if a.stop != "" {
		beat.CancelRequested, beat.CancelReason = true, a.stop
	}
	return fundedBoundTx(ctx, tx, team, unit, generation, now)
}

func stopAwardTx(ctx context.Context, tx *storeTx, team Team, a billingAward, reason string, drain time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE billing_awards SET stop_reason=?,drain_deadline_at=? WHERE team=? AND reservation_id=? AND stop_reason=''`, reason, drain.UnixNano(), team, a.provenance.ReservationID); err != nil {
		return err
	}
	run, err := readRunTx(ctx, tx, team, a.run)
	if err != nil || run.FinishedAt != nil || run.CancelRequestedAt != nil {
		return err
	}
	previous := tx.cancelingTeam
	tx.cancelingTeam = team
	defer func() { tx.cancelingTeam = previous }()
	_, err = transitionRunTx(ctx, tx, team, RunTransition{RunID: a.run, To: RunCancelled, Actor: Actor{Kind: ActorOperator}, Cause: RunCancel, Reason: reason}, tx.now())
	return err
}

func transitionNodeBillingTx(ctx context.Context, tx *storeTx, team Team, row NodeRow, change NodeTransition, edge NodeEdge, now time.Time) error {
	if row.ClaimedBy == "" {
		return nil
	}
	if edge.RequestOnly {
		if change.Cause != CauseCancel {
			return nil
		}
		return endUnstartedBillingTx(ctx, tx, team, row.Ref.RunID, &row.Ref.NodeID)
	}
	unit := UnitRef{Kind: capability.UnitNode, RunID: row.Ref.RunID, NodeID: row.Ref.NodeID}
	if change.To == NodeRunning {
		if change.Cause == CauseRetry {
			if err := computeUnitBillingTx(ctx, tx, team, unit, row.Generation, row.Attempt, false); err != nil {
				return err
			}
		}
		return computeUnitBillingTx(ctx, tx, team, unit, row.Generation, row.Attempt+1, true)
	}
	if change.To == NodePending || terminalNodeState(change.To) {
		if change.Cause == CauseLeaseLapse && row.LeaseExpiresAt != nil && row.LeaseExpiresAt.Before(now) {
			now = *row.LeaseExpiresAt
		}
		return settleUnitBillingTx(ctx, tx, team, unit, row.Generation, now)
	}
	return nil
}

func endUnstartedBillingTx(ctx context.Context, tx *storeTx, team Team, run string, node *string) error {
	query := `SELECT reservation_id FROM billing_awards a WHERE team=? AND run_id=? AND metered=1 AND settled_at IS NULL AND NOT EXISTS (SELECT 1 FROM billable_intervals i WHERE i.team=a.team AND i.reservation_id=a.reservation_id)`
	args := []any{team, run}
	if node != nil {
		query += ` AND node_id=? AND unit_kind=?`
		args = append(args, *node, capability.UnitNode)
	}
	rows, err := tx.QueryContext(ctx, query+` ORDER BY reservation_id`, args...)
	if err != nil {
		return err
	}
	var unstarted []string
	for rows.Next() {
		var reservation string
		if err := rows.Scan(&reservation); err != nil {
			return errors.Join(err, rows.Close())
		}
		unstarted = append(unstarted, reservation)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, reservation := range unstarted {
		if _, err := accountBillingAwardTx(ctx, tx, team, reservation, tx.now(), true); err != nil {
			return err
		}
	}
	return nil
}

func terminalNodeState(state NodeState) bool {
	return state == NodeSuccess || state == NodeFailed || state == NodeCancelled || state == NodeSkipped || state == NodeSuperseded || state == NodeMemoized
}

func billingRecoveryEnd(owner ownerRecovery, now time.Time) time.Time {
	if owner.lease.Valid && owner.lease.Int64 < now.UnixNano() {
		return time.Unix(0, owner.lease.Int64).UTC()
	}
	return now
}

func ownerUnitTx(ctx context.Context, tx *storeTx, team Team, run string) (UnitRef, error) {
	var kind capability.UnitKind
	err := tx.QueryRowContext(ctx, `SELECT unit_kind FROM triggers WHERE team=? AND id=?`, team, run).Scan(&kind)
	return UnitRef{Kind: kind, RunID: run}, err
}

func settleOwnerBillingTx(ctx context.Context, tx *storeTx, team Team, run string, generation int64, through time.Time) error {
	unit, err := ownerUnitTx(ctx, tx, team, run)
	if err != nil {
		return err
	}
	return settleUnitBillingTx(ctx, tx, team, unit, generation, through)
}

func finishExpansionBillingTx(ctx context.Context, tx *storeTx, team Team, request ExpansionSubmission) error {
	c, err := loadCapabilityTx(ctx, tx, request.Actor.CapabilityPrefix)
	if err != nil {
		return err
	}
	lease := c.Binding.Expansion
	if lease == nil || lease.Kind != capability.UnitEval {
		return nil
	}
	unit := UnitRef{Kind: capability.UnitEval, RunID: request.Source.RunID, NodeID: request.Source.NodeID, SourceGeneration: request.SourceGeneration}
	return settleUnitBillingTx(ctx, tx, team, unit, lease.Generation, tx.now())
}

func renewOwnedNodesBillingTx(ctx context.Context, tx *storeTx, team Team, actor Actor, now time.Time, beat *LeaseBeat) ([]time.Time, error) {
	if actor.CapabilityPrefix == "" {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT node_id,generation FROM nodes WHERE team=? AND run_id=? AND owner_capability_prefix=? AND claimed_by=? AND status<>'done' AND lease_expires_at>? ORDER BY node_id`, team, actor.Owner.RunID, actor.CapabilityPrefix, actor.Owner.Holder, now.UnixNano())
	if err != nil {
		return nil, err
	}
	type held struct {
		node       string
		generation int64
	}
	var awards []held
	for rows.Next() {
		var award held
		if err := rows.Scan(&award.node, &award.generation); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		awards = append(awards, award)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var bounds []time.Time
	for _, award := range awards {
		bound, err := renewUnitBillingTx(ctx, tx, team, UnitRef{Kind: capability.UnitNode, RunID: actor.Owner.RunID, NodeID: award.node}, award.generation, beat)
		if err != nil {
			return nil, err
		}
		bounds = append(bounds, bound)
	}
	return bounds, nil
}
