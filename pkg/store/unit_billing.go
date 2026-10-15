package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

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
		resources := run.Request.Request.Options.Resources.Value
		if run.Spec != nil {
			resources = run.Spec.Resolved.Settings.Options.Resources.Value
		}
		if c.Unit == capability.UnitNode {
			row, err := readNodeTx(ctx, tx, Team(c.Team), NodeRef{c.Binding.RunID, c.Binding.NodeID})
			if err != nil {
				return err
			}
			resources = row.Execution.Resources.Value
		}
		price, err = unitBillingPriceTx(ctx, tx, matchingResources(resources))
		if err != nil {
			return err
		}
		reservedSeconds = MinBillableSeconds
	}
	return recordBillingAwardTx(ctx, tx, c, price, reservedSeconds)
}

func billingUnitKind(kind capability.UnitKind) capability.BillingUnit {
	for _, mapping := range capability.UnitBillingMappings {
		if mapping.Operational == kind {
			return mapping.Billing
		}
	}
	return ""
}

func unitBillingPriceTx(ctx context.Context, tx *storeTx, resources ExecutorResource) (CreditRate, error) {
	table, err := creditRateTableTx(ctx, tx)
	if err != nil {
		return CreditRate{}, err
	}
	return table.ClassForResource(resources)
}

func eligibleUnitBillingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, resources ExecutorResource) error {
	price, err := unitBillingPriceTx(ctx, tx, resources)
	if err != nil {
		return err
	}
	amount, err := billingProduct(MinBillableSeconds, price.MicroPerSecond)
	if err != nil {
		return err
	}
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil {
		return err
	}
	if err := refuseFrozenTeamTx(ctx, tx, team, balance, amount, unit.RunID, unit.NodeID); err != nil {
		return err
	}
	return refuseAboveHeadroomTx(ctx, tx, team, amount, tx.now(), unit.RunID, unit.NodeID)
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
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
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

func accountUnitBillingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, generation int64, through time.Time, final bool) error {
	a, err := unitBillingAwardTx(ctx, tx, team, unit, generation)
	if err != nil || a == nil || a.settled.Valid {
		return err
	}
	calculate := tx.store.billing
	if calculate == nil {
		calculate = capability.SettleBilling
	}
	if !a.provenance.Metered {
		calculate = func(p capability.BillingProvenance, _ []capability.BillableInterval) (capability.BillingSettlement, error) {
			return capability.BillingSettlement{ReservationID: p.ReservationID}, nil
		}
	}
	_, err = accountBillingAwardTx(ctx, tx, team, a.provenance.ReservationID, through, final, calculate)
	return err
}

func transitionNodeBillingTx(ctx context.Context, tx *storeTx, team Team, row NodeRow, change NodeTransition, edge NodeEdge, now time.Time) error {
	if edge.RequestOnly || row.ClaimedBy == "" {
		return nil
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
		return accountUnitBillingTx(ctx, tx, team, unit, row.Generation, now, true)
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

func accountOwnerBillingTx(ctx context.Context, tx *storeTx, team Team, run string, generation int64, now time.Time, final bool) error {
	var kind capability.UnitKind
	if err := tx.QueryRowContext(ctx, `SELECT unit_kind FROM triggers WHERE team=? AND id=?`, team, run).Scan(&kind); err != nil {
		return err
	}
	return accountUnitBillingTx(ctx, tx, team, UnitRef{Kind: kind, RunID: run}, generation, now, final)
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
	return accountUnitBillingTx(ctx, tx, team, unit, lease.Generation, tx.now(), true)
}

func chargeOwnedNodesBillingTx(ctx context.Context, tx *storeTx, team Team, actor Actor, now time.Time) error {
	if actor.CapabilityPrefix == "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT node_id,generation FROM nodes WHERE team=? AND run_id=? AND owner_capability_prefix=? AND claimed_by=? AND status<>'done' AND lease_expires_at>? ORDER BY node_id`, team, actor.Owner.RunID, actor.CapabilityPrefix, actor.Owner.Holder, now.UnixNano())
	if err != nil {
		return err
	}
	type held struct {
		node       string
		generation int64
	}
	var awards []held
	for rows.Next() {
		var award held
		if err := rows.Scan(&award.node, &award.generation); err != nil {
			return errors.Join(err, rows.Close())
		}
		awards = append(awards, award)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, award := range awards {
		if err := accountUnitBillingTx(ctx, tx, team, UnitRef{Kind: capability.UnitNode, RunID: actor.Owner.RunID, NodeID: award.node}, award.generation, now, false); err != nil {
			return err
		}
	}
	return nil
}
