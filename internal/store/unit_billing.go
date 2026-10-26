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
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func meteredPlacement(claimant, root capability.Capability) bool {
	return capability.CloudLauncher(claimant) || !launcherClaimant(claimant) && root.RootMetered
}

func persistUnitBillingTx(ctx context.Context, tx *storeTx, c capability.Capability, selected *CreditRate) error {
	if c.Purpose == capability.PurposeHere {
		return nil
	}
	var price CreditRate
	var reservedSeconds int64
	if c.Billing.Metered {
		if selected == nil {
			return authorityError(apierr.IntegrityFailure, "metered award is missing its selected size price")
		}
		price = *selected
		l, err := lockLedgerTx(ctx, tx, Team(c.Team))
		if err != nil {
			return err
		}
		if c.Billing.ExecutorClass == TierCloud {
			if err := checkOverflowTx(ctx, tx, Team(c.Team), c.Binding.RunID); err != nil {
				return err
			}
			if err := checkCloudCapacityTx(ctx, l.view(), price.Cores); err != nil {
				return err
			}
		}
		reservedSeconds = SliceSeconds
	}
	return recordBillingAwardTx(ctx, tx, c, price, reservedSeconds)
}

func unitSelectorsTx(ctx context.Context, tx *storeTx, team Team, run RunRow, unit UnitRef) ([]string, error) {
	if unit.Kind == capability.UnitNode {
		row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
		return row.Execution.Requires.Value, err
	}
	if run.Spec != nil {
		return run.Spec.Resolved.Settings.Options.Requires.Value, nil
	}
	return run.Request.Request.Options.Requires.Value, nil
}

func billingUnitKind(kind capability.UnitKind) capability.BillingUnit {
	for _, mapping := range capability.UnitBillingMappings() {
		if mapping.Operational == kind {
			return mapping.Billing
		}
	}
	return ""
}

func unitBillingPriceTx(ctx context.Context, tx *storeTx, selectors []string, profile match.Profile) (CreditRate, error) {
	table, err := creditRateTableTx(ctx, tx)
	if err != nil {
		return CreditRate{}, err
	}
	return table.ClassForLabels(selectors, profile)
}

func checkUnitBillingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, price CreditRate, cloud bool) error {
	view := ledgerView{tx: tx, team: team}
	if cloud {
		if err := checkOverflowTx(ctx, tx, team, string(unit.RunID)); err != nil {
			return err
		}
		if err := checkCloudCapacityTx(ctx, view, price.Cores); err != nil {
			return err
		}
	}
	return unitCreditRefusalViewTx(ctx, view, unit, price)
}

func unitCreditRefusalTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, price CreditRate) error {
	return unitCreditRefusalViewTx(ctx, ledgerView{tx: tx, team: team}, unit, price)
}

func unitCreditRefusalViewTx(ctx context.Context, view ledgerView, unit UnitRef, price CreditRate) error {
	amount, err := billingProduct(SliceSeconds, price.MicroPerSecond)
	if err != nil {
		return err
	}
	return refuseUnfundedTx(ctx, view, amount, string(unit.RunID), string(unit.NodeID))
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
		return startBillableIntervalTx(ctx, tx, team, a.provenance.ReservationID, fmt.Sprintf("compute:%d", count), attempt, tx.now(ctx))
	}
	key := fmt.Sprintf("compute:%d", count-1)
	if active == billingID("interval", team, a.provenance.ReservationID, "award") {
		key = "award"
	}
	return finishBillableIntervalTx(ctx, tx, team, a.provenance.ReservationID, key, tx.now(ctx))
}

func settleUnitBillingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, generation int64, through time.Time) error {
	a, err := unitBillingAwardTx(ctx, tx, team, unit, generation)
	if err != nil || a == nil || a.settled.Valid {
		return err
	}
	_, err = accountBillingAwardTx(ctx, tx, team, a.provenance.ReservationID, through, true)
	return err
}

// Purge closes accounting while the work rows still exist. Retained awards
// and intervals then contain everything needed for late financial callbacks.
func settleTeamAwardsTx(ctx context.Context, tx *storeTx, team Team) error {
	rows, err := tx.queryContext(ctx, `SELECT reservation_id FROM billing_awards WHERE team=? AND settled_at IS NULL ORDER BY reservation_id`, team)
	if err != nil {
		return err
	}
	reservations, err := scanPrefixes(rows)
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if _, err := accountBillingAwardTx(ctx, tx, team, reservation, tx.now(ctx), true); err != nil {
			return err
		}
	}
	return nil
}

// safety: a renewal charges what the work used and pays for its next slice in one step. A balance or hold that cannot
// pay writes the award's durable stop, a drain deadline at most one slice out and the job's cancellation in the same
// transaction, so every later renewal reports the refusal and no lease outlives the drain. It returns the unit's
// funded bound for leaseBoundTx.
func renewUnitBillingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, generation int64, beat *runstate.LeaseBeat) (time.Time, error) {
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
		return fundedBoundTx(ctx, tx, team, unit, generation, tx.now(ctx))
	}
	now := tx.now(ctx)
	err = renewBillingAwardTx(ctx, tx, team, a.provenance.ReservationID, now, a.provenance.Metered && !a.drain.Valid)
	if refused := (*InsufficientCreditsError)(nil); errors.As(err, &refused) {
		drain := now.Add(SliceSeconds * time.Second)
		if refused.Hold != "" {
			mode, modeErr := creditPaymentMode(ctx, tx)
			if modeErr != nil {
				return time.Time{}, modeErr
			}
			var deadline sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT MIN(deadline) FROM (
 SELECT drain_deadline_at AS deadline FROM credit_freezes WHERE team=? AND payment_mode=? AND released_at IS NULL
 UNION ALL SELECT billing_trust_at+? FROM teams WHERE name=? AND billing_trust='revoked' AND billing_trust_at>0) drains`, team, mode, int64(SliceSeconds*time.Second), team).Scan(&deadline); err != nil {
				return time.Time{}, err
			}
			if deadline.Valid && deadline.Int64 < drain.UnixNano() {
				drain = time.Unix(0, deadline.Int64).UTC()
			}
		}
		err = stopAwardTx(ctx, tx, team, *a, refused.Error(), drain)
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
	if _, err := tx.execContext(ctx, `UPDATE billing_awards SET stop_reason=?,drain_deadline_at=? WHERE team=? AND reservation_id=? AND stop_reason=''`, reason, drain.UnixNano(), team, a.provenance.ReservationID); err != nil {
		return err
	}
	previous := tx.cancelingTeam
	tx.cancelingTeam = team
	defer func() { tx.cancelingTeam = previous }()
	if a.unit == capability.UnitTrigger || a.unit == capability.UnitPlan {
		run, err := readRunTx(ctx, tx, team, a.run)
		if err != nil {
			return err
		}
		if run.FinishedAt == nil && run.CancelRequestedAt == nil {
			if _, err := transitionRunTx(ctx, tx, team, runstate.RunTransition{RunID: runspec.RunID(a.run), To: runstate.RunCancelled, Actor: runstate.Actor{Kind: runstate.ActorOperator}, Cause: runstate.RunCancel, Reason: reason}); err != nil {
				return err
			}
		}
	}
	if a.unit != capability.UnitNode {
		var intervals int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM billable_intervals WHERE team=? AND reservation_id=?`, team, a.provenance.ReservationID).Scan(&intervals); err != nil {
			return err
		}
		if intervals == 0 {
			_, err := accountBillingAwardTx(ctx, tx, team, a.provenance.ReservationID, tx.now(ctx), true)
			return err
		}
		return nil
	}
	ref := runspec.NodeRef{RunID: runspec.RunID(a.run), NodeID: runspec.NodeID(a.node)}
	row, err := readNodeTx(ctx, tx, team, ref)
	if err != nil || row.Status == runstate.StatusDone || row.Generation != runspec.ClaimGeneration(a.generation) || row.CancelRequestedAt != nil {
		return err
	}

	_, err = transitionNodeStateTx(ctx, tx, team, nodeTransition{Ref: ref, To: runstate.NodeCancelled, Actor: runstate.Actor{Kind: runstate.ActorOperator}, Cause: causeCancel, Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeCancelled, Reason: reason}}})
	return err
}

func transitionNodeBillingTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow, change nodeTransition, edge nodeEdge, now time.Time) error {
	if row.ClaimedBy == "" {
		return nil
	}
	if edge.RequestOnly {
		if change.Cause != causeCancel {
			return nil
		}
		return endUnstartedBillingTx(ctx, tx, team, string(row.Ref.RunID), (*string)(&row.Ref.NodeID))
	}
	unit := UnitRef{Kind: capability.UnitNode, RunID: row.Ref.RunID, NodeID: row.Ref.NodeID}
	if change.To == runstate.NodeRunning {
		if change.Cause == runstate.CauseRetry {
			if err := computeUnitBillingTx(ctx, tx, team, unit, int64(row.Generation), int64(row.Attempt), false); err != nil {
				return err
			}
		}
		return computeUnitBillingTx(ctx, tx, team, unit, int64(row.Generation), int64(row.Attempt+1), true)
	}
	if change.To == nodePending || terminalNodeState(change.To) {
		if change.Cause == runstate.CauseLeaseLapse && row.LeaseExpiresAt != nil && row.LeaseExpiresAt.Before(now) {
			now = *row.LeaseExpiresAt
		}
		return settleUnitBillingTx(ctx, tx, team, unit, int64(row.Generation), now)
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
	rows, err := tx.queryContext(ctx, query+` ORDER BY reservation_id`, args...)
	if err != nil {
		return err
	}
	unstarted, err := scanPrefixes(rows)
	if err != nil {
		return err
	}
	for _, reservation := range unstarted {
		if _, err := accountBillingAwardTx(ctx, tx, team, reservation, tx.now(ctx), true); err != nil {
			return err
		}
	}
	return nil
}

func terminalNodeState(state runstate.NodeState) bool {
	return state == runstate.NodeSuccess || state == runstate.NodeFailed || state == runstate.NodeCancelled || state == runstate.NodeSkipped || state == runstate.NodeSuperseded || state == runstate.NodeMemoized
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
	return UnitRef{Kind: kind, RunID: runspec.RunID(run)}, err
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
	unit := UnitRef{Kind: capability.UnitEval, RunID: request.Source.RunID, NodeID: request.Source.NodeID, SourceGeneration: int64(request.SourceGeneration)}
	return settleUnitBillingTx(ctx, tx, team, unit, lease.Generation, tx.now(ctx))
}

func renewOwnedNodesBillingTx(ctx context.Context, tx *storeTx, team Team, actor runstate.Actor, now time.Time) ([]time.Time, error) {
	if actor.CapabilityPrefix == "" {
		return nil, nil
	}
	rows, err := tx.queryContext(ctx, `SELECT node_id,generation,lease_expires_at,capability_prefix FROM nodes WHERE team=? AND run_id=? AND owner_capability_prefix=? AND claimed_by=? AND status<>'done' AND cancel_requested_at IS NULL ORDER BY node_id`, team, actor.Owner.RunID, actor.CapabilityPrefix, actor.Owner.Holder)
	if err != nil {
		return nil, err
	}
	type held struct {
		node       string
		generation int64
		expiry     int64
		prefix     string
	}
	var awards []held
	for rows.Next() {
		var award held
		if err := rows.Scan(&award.node, &award.generation, &award.expiry, &award.prefix); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if award.expiry <= now.UnixNano() {
			return nil, errors.Join(holderRefusal(), rows.Close())
		}
		awards = append(awards, award)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var bounds []time.Time
	for _, award := range awards {
		var beat runstate.LeaseBeat
		bound, err := renewUnitBillingTx(ctx, tx, team, UnitRef{Kind: capability.UnitNode, RunID: actor.Owner.RunID, NodeID: runspec.NodeID(award.node)}, award.generation, &beat)
		if err != nil {
			return nil, err
		}
		var previous time.Time
		if beat.CancelRequested {
			previous = time.Unix(0, award.expiry)
		}
		bound, err = leaseBoundTx(ctx, tx, award.prefix, now, bound, previous)
		if err != nil {
			return nil, err
		}
		if beat.CancelRequested {
			if _, err := tx.execContext(ctx, `UPDATE nodes SET lease_expires_at=? WHERE team=? AND run_id=? AND node_id=? AND generation=?`, bound.UnixNano(), team, actor.Owner.RunID, award.node, award.generation); err != nil {
				return nil, err
			}
			continue
		}
		bounds = append(bounds, bound)
	}
	return bounds, nil
}
