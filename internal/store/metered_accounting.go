package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type billingAward struct {
	team                Team
	provenance          capability.BillingProvenance
	prefix, run, node   string
	unit                capability.UnitKind
	generation, awarded int64
	settled             sql.NullInt64
	// safety: stop is the durable funding refusal, never cleared; from then on drain bounds every lease.
	stop  string
	drain sql.NullInt64
}

func billingID(kind string, team Team, reservation, key string) string {
	encoded, _ := json.Marshal([]string{kind, string(team), reservation, key})
	sum := sha256.Sum256(encoded)
	return kind + ":" + hex.EncodeToString(sum[:])
}

func billingProduct(seconds, rate int64) (int64, error) {
	if seconds < 0 || rate < 0 || (rate > 0 && seconds > math.MaxInt64/rate) {
		return 0, authorityError(apierr.BadRequest, "billing amount is out of range")
	}
	return seconds * rate, nil
}

func readBillingAwardTx(ctx context.Context, tx *storeTx, team Team, reservation string) (billingAward, error) {
	a := billingAward{team: team}
	a.provenance.ReservationID = reservation
	err := tx.QueryRowContext(ctx, `SELECT capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,settled_at,stop_reason,drain_deadline_at FROM billing_awards WHERE team=? AND reservation_id=?`+tx.forUpdate(), team, reservation).Scan(&a.prefix, &a.run, &a.node, &a.generation, &a.provenance.PayerTeam, &a.provenance.Metered, &a.provenance.ExecutorClass, &a.provenance.Start, &a.unit, &a.provenance.Unit, &a.awarded, &a.settled, &a.stop, &a.drain)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// safety: issuance supplies server-derived provenance and pricing while holding the claim locks.
// The reservation charge is also the immutable price snapshot, including a zero reservation.
func recordBillingAwardTx(ctx context.Context, tx *storeTx, c capability.Capability, price CreditRate, reserveSeconds int64) error {
	b := c.Billing
	if b == nil || b.ReservationID == "" || c.Team == "" || b.PayerTeam != c.Team || c.Prefix == "" || c.Binding.RunID == "" || b.ExecutorClass == "" || (b.Start != capability.BillingAtAward && b.Start != capability.BillingAtStart) {
		return authorityError(apierr.BadRequest, "invalid billing award")
	}
	mapping := false
	for _, m := range capability.UnitBillingMappings() {
		if m.Operational == c.Unit && m.Billing == b.Unit {
			mapping = true
		}
	}
	if !mapping {
		return authorityError(apierr.BadRequest, "billing unit differs from operational unit")
	}
	generation := c.Binding.Generation
	if c.Binding.Expansion != nil {
		generation = c.Binding.Expansion.Generation
	}
	if generation <= 0 {
		return authorityError(apierr.BadRequest, "billing award has no generation")
	}
	amount, err := billingProduct(reserveSeconds, price.MicroPerSecond)
	if err != nil {
		return err
	}
	if b.Metered && (price.Cores <= 0 || price.MicroPerSecond <= 0) {
		return authorityError(apierr.BadRequest, "metered award has no price")
	}
	if !b.Metered {
		amount, reserveSeconds = 0, 0
	}
	l, err := lockLedgerTx(ctx, tx, Team(c.Team))
	if err != nil {
		return err
	}
	existing, err := readBillingAwardTx(ctx, tx, Team(c.Team), b.ReservationID)
	if err == nil {
		if existing.provenance != *b || existing.prefix != c.Prefix || existing.run != c.Binding.RunID || existing.node != c.Binding.NodeID || existing.generation != generation || existing.unit != c.Unit {
			return authorityError(apierr.Conflict, "billing award is immutable")
		}
		if b.Metered {
			rate, class, seconds, err := billingPriceTx(ctx, tx, existing)
			if err != nil {
				return err
			}
			if rate != price.MicroPerSecond || class != price.Cores || seconds != reserveSeconds {
				return authorityError(apierr.Conflict, "reservation price is immutable")
			}
		}
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	var reused int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM billing_awards WHERE team=? AND capability_prefix=?", c.Team, c.Prefix).Scan(&reused); err != nil {
		return err
	}
	if reused != 0 {
		return authorityError(apierr.Conflict, "capability already has a billing award")
	}
	if b.Metered {
		if err := refuseUnfundedTx(ctx, l.view(), amount, c.Binding.RunID, c.Binding.NodeID); err != nil {
			return err
		}
	}
	now := tx.now(ctx).UnixNano()
	var requester string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT CASE WHEN created_subject_kind='account' THEN created_principal ELSE '' END FROM runs WHERE team=? AND id=?),'')`, c.Team, c.Binding.RunID).Scan(&requester); err != nil {
		return err
	}
	_, err = tx.execContext(ctx, `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,requester_account) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.Team, b.ReservationID, c.Prefix, c.Binding.RunID, c.Binding.NodeID, generation, b.PayerTeam, boolInt(b.Metered), b.ExecutorClass, b.Start, c.Unit, b.Unit, now, requester)
	if err != nil {
		return err
	}
	if b.Metered {
		if err := l.insertCharge(ctx, CreditCharge{
			ID: billingID("reservation", Team(c.Team), b.ReservationID, ""), Team: Team(c.Team), RunID: c.Binding.RunID,
			NodeID: c.Binding.NodeID, TokenPrefix: c.Prefix, Principal: c.Subject.ID, Kind: creditChargeReservation,
			Seconds: reserveSeconds, AmountMicro: amount, CPUClassCores: price.Cores, RateMicroPerSecond: price.MicroPerSecond,
			ChargedAt: time.Unix(0, now),
		}); err != nil {
			return err
		}
	}
	if b.Start == capability.BillingAtAward {
		return startBillableIntervalTx(ctx, tx, Team(c.Team), b.ReservationID, "award", 0, tx.now(ctx))
	}
	return nil
}

func startBillableIntervalTx(ctx context.Context, tx *storeTx, team Team, reservation, key string, attempt int64, at time.Time) error {
	if key == "" || attempt < 0 {
		return authorityError(apierr.BadRequest, "invalid compute interval identity")
	}
	a, err := readBillingAwardTx(ctx, tx, team, reservation)
	if err != nil {
		return err
	}
	if a.settled.Valid {
		return authorityError(apierr.Conflict, "billing award is settled")
	}
	if at.After(tx.now(ctx)) || at.UnixNano() < a.awarded {
		return authorityError(apierr.BadRequest, "compute start is outside its awarded time range")
	}
	if !a.provenance.Metered {
		return nil
	}
	id := billingID("interval", team, reservation, key)
	var started, priorAttempt int64
	err = tx.QueryRowContext(ctx, "SELECT started_at,attempt FROM billable_intervals WHERE team=? AND reservation_id=? AND id=?", team, reservation, id).Scan(&started, &priorAttempt)
	if err == nil {
		if started != at.UnixNano() || priorAttempt != attempt {
			return authorityError(apierr.Conflict, "compute interval identity has conflicting content")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var overlap int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM billable_intervals WHERE team=? AND reservation_id=? AND (finished_at IS NULL OR finished_at>?)", team, reservation, at.UnixNano()).Scan(&overlap); err != nil {
		return err
	}
	if overlap != 0 {
		return authorityError(apierr.Conflict, "compute intervals overlap")
	}
	_, err = tx.execContext(ctx, `INSERT INTO billable_intervals(team,id,run_id,node_id,attempt,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,started_at,charged_through) VALUES (?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?)`, team, id, a.run, a.node, attempt, a.generation, a.prefix, reservation, a.provenance.PayerTeam, a.provenance.ExecutorClass, a.unit, a.provenance.Unit, a.provenance.Start, at.UnixNano(), at.UnixNano())
	return err
}

func finishBillableIntervalTx(ctx context.Context, tx *storeTx, team Team, reservation, key string, at time.Time) error {
	a, err := readBillingAwardTx(ctx, tx, team, reservation)
	if err != nil {
		return err
	}
	if !a.provenance.Metered {
		return nil
	}
	id := billingID("interval", team, reservation, key)
	var start, charged int64
	var end sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT started_at,finished_at,charged_through FROM billable_intervals WHERE team=? AND reservation_id=? AND id=?", team, reservation, id).Scan(&start, &end, &charged); err != nil {
		return err
	}
	if at.After(tx.now(ctx)) || at.UnixNano() < start {
		return authorityError(apierr.BadRequest, "compute finish is outside its started time range")
	}
	if end.Valid {
		if end.Int64 != at.UnixNano() {
			return authorityError(apierr.Conflict, "compute finish is immutable")
		}
		return nil
	}
	if a.settled.Valid {
		return authorityError(apierr.Conflict, "billing award is settled")
	}
	if at.UnixNano() < charged {
		return authorityError(apierr.Conflict, "compute finish precedes its charged checkpoint")
	}
	_, err = tx.execContext(ctx, "UPDATE billable_intervals SET finished_at=? WHERE team=? AND reservation_id=? AND id=?", at.UnixNano(), team, reservation, id)
	return err
}

func accountBillingAwardTx(ctx context.Context, tx *storeTx, team Team, reservation string, through time.Time, final bool) (capability.BillingSettlement, error) {
	l, err := lockLedgerTx(ctx, tx, team)
	if err != nil {
		return capability.BillingSettlement{}, err
	}
	a, err := readBillingAwardTx(ctx, tx, team, reservation)
	if err != nil {
		return capability.BillingSettlement{}, err
	}
	if a.settled.Valid {
		return settledBillingResultTx(ctx, tx, a)
	}
	if through.After(tx.now(ctx)) || through.UnixNano() < a.awarded {
		return capability.BillingSettlement{}, authorityError(apierr.BadRequest, "billing checkpoint is outside its awarded time range")
	}
	intervals, err := billingIntervalsTx(ctx, tx, a, through, final)
	if err != nil {
		return capability.BillingSettlement{}, err
	}
	result := capability.BillingSettlement{ReservationID: a.provenance.ReservationID}
	if a.provenance.Metered {
		result, err = capability.SettleBilling(a.provenance, intervals)
		if err != nil {
			return result, authorityError(apierr.IntegrityFailure, "invalid persisted billing: "+err.Error())
		}
		if err := validateBillingCalculation(a, intervals, result); err != nil {
			return result, err
		}
		if err := settleBillingChargesTx(ctx, l, a, result, through, final); err != nil {
			return result, err
		}
	}
	if !final {
		return result, nil
	}
	_, err = tx.execContext(ctx, "UPDATE billing_awards SET settled_at=? WHERE team=? AND reservation_id=? AND settled_at IS NULL", tx.now(ctx).UnixNano(), team, reservation)
	return result, err
}

// safety: balance checks and slice debits share the team lock and startup payment mode.
// Cutover requires settled awards; a refusal still charges work already used.
func renewBillingAwardTx(ctx context.Context, tx *storeTx, team Team, reservation string, through time.Time, reserve bool) error {
	result, err := accountBillingAwardTx(ctx, tx, team, reservation, through, false)
	if err != nil || !reserve {
		return err
	}
	l, err := lockLedgerTx(ctx, tx, team)
	if err != nil {
		return err
	}
	a, err := readBillingAwardTx(ctx, tx, team, reservation)
	if err != nil {
		return err
	}
	rate, class, _, err := billingPriceTx(ctx, tx, a)
	if err != nil {
		return err
	}
	paid, paidSeconds, err := paidBillingTx(ctx, tx, a)
	if err != nil {
		return err
	}
	want, err := billingProduct(result.BillableSeconds+SliceSeconds, rate)
	if err != nil {
		return err
	}
	if err := refuseUnfundedTx(ctx, l.view(), max(want-paid, 0), a.run, a.node); err != nil {
		return err
	}
	ahead := result.BillableSeconds + SliceSeconds - paidSeconds
	if ahead <= 0 {
		return nil
	}
	return l.insertCharge(ctx, CreditCharge{
		ID: billingID("slice", team, reservation, through.UTC().Format(time.RFC3339Nano)), Team: team, RunID: a.run,
		NodeID: a.node, TokenPrefix: a.prefix, Kind: creditChargeSlice, Seconds: ahead, AmountMicro: want - paid,
		CPUClassCores: class, RateMicroPerSecond: rate, ChargedAt: tx.now(ctx),
	})
}

func validateBillingCalculation(a billingAward, intervals []capability.BillableInterval, result capability.BillingSettlement) error {
	if result.ReservationID != a.provenance.ReservationID || result.BillableSeconds < 0 || (result.RefundUnstarted && (len(intervals) > 0 || result.BillableSeconds != 0)) || (!a.provenance.Metered && result.BillableSeconds != 0) {
		return authorityError(apierr.IntegrityFailure, "invalid billing calculation")
	}
	return nil
}

func readBillingIntervalsTx(ctx context.Context, tx *storeTx, a billingAward, through time.Time, checkpoint bool) ([]capability.BillableInterval, error) {
	rows, err := tx.queryContext(ctx, "SELECT capability_prefix,billing_unit,started_at,finished_at,charged_through FROM billable_intervals WHERE team=? AND reservation_id=? ORDER BY started_at,id", a.team, a.provenance.ReservationID)
	if err != nil {
		return nil, err
	}
	var intervals []capability.BillableInterval
	for rows.Next() {
		var i capability.BillableInterval
		var start, charged int64
		var end sql.NullInt64
		if err := rows.Scan(&i.CapabilityPrefix, &i.Unit, &start, &end, &charged); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		i.Start = time.Unix(0, start).UTC()
		i.End = through
		if end.Valid {
			i.End = time.Unix(0, end.Int64).UTC()
		} else if checkpoint {
			i.End = time.Unix(0, charged).UTC()
		}
		if charged > through.UnixNano() || i.End.Before(i.Start) || i.End.After(through) || i.CapabilityPrefix != a.prefix || i.Unit != a.provenance.Unit {
			return nil, errors.Join(authorityError(apierr.IntegrityFailure, "persisted interval conflicts with settlement"), rows.Close())
		}
		intervals = append(intervals, i)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return intervals, nil
}

func billingIntervalsTx(ctx context.Context, tx *storeTx, a billingAward, through time.Time, final bool) ([]capability.BillableInterval, error) {
	intervals, err := readBillingIntervalsTx(ctx, tx, a, through, false)
	if err != nil {
		return nil, err
	}
	if final {
		_, err = tx.execContext(ctx, "UPDATE billable_intervals SET finished_at=COALESCE(finished_at,?),charged_through=COALESCE(finished_at,?) WHERE team=? AND reservation_id=?", through.UnixNano(), through.UnixNano(), a.team, a.provenance.ReservationID)
	} else {
		_, err = tx.execContext(ctx, "UPDATE billable_intervals SET charged_through=COALESCE(finished_at,?) WHERE team=? AND reservation_id=?", through.UnixNano(), a.team, a.provenance.ReservationID)
	}
	return intervals, err
}

func billingPriceTx(ctx context.Context, tx *storeTx, a billingAward) (rate, class, seconds int64, err error) {
	err = tx.QueryRowContext(ctx, "SELECT rate_micro_per_second,cpu_class,seconds FROM credit_charges WHERE team=? AND id=? AND kind='reservation'", a.team, billingID("reservation", a.team, a.provenance.ReservationID, "")).Scan(&rate, &class, &seconds)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (seconds < 0 || rate < 0 || class < 0 || a.provenance.Metered && (rate == 0 || class == 0)) {
		err = authorityError(apierr.IntegrityFailure, "invalid persisted reservation price")
	}
	return rate, class, seconds, err
}

func paidBillingTx(ctx context.Context, tx *storeTx, a billingAward) (paid, seconds int64, err error) {
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount_micro),0),COALESCE(SUM(seconds),0) FROM credit_charges WHERE team=? AND capability_prefix=? AND run_id=? AND node_id=? AND kind IN ('reservation','slice','usage','refund')", a.team, a.prefix, a.run, a.node).Scan(&paid, &seconds)
	return paid, seconds, err
}

func settleBillingChargesTx(ctx context.Context, l *ledgerTx, a billingAward, result capability.BillingSettlement, through time.Time, final bool) error {
	rate, class, _, err := billingPriceTx(ctx, l.storeTx, a)
	if err != nil {
		return err
	}
	total, err := billingProduct(result.BillableSeconds, rate)
	if err != nil {
		return err
	}
	paid, paidSeconds, err := paidBillingTx(ctx, l.storeTx, a)
	if err != nil {
		return err
	}
	delta := total - paid
	if !final && delta <= 0 {
		return nil
	}
	id := billingID("settlement", a.team, a.provenance.ReservationID, "")
	if !final {
		id = billingID("checkpoint", a.team, a.provenance.ReservationID, through.UTC().Format(time.RFC3339Nano))
	}
	chargedAt := l.now(ctx)
	kind := creditChargeUsage
	if delta < 0 {
		kind, chargedAt = creditChargeRefund, time.Unix(0, a.awarded)
	}
	return l.insertCharge(ctx, CreditCharge{
		ID: id, Team: a.team, RunID: a.run, NodeID: a.node, TokenPrefix: a.prefix, Kind: kind,
		Seconds: result.BillableSeconds - paidSeconds, AmountMicro: delta, CPUClassCores: class, RateMicroPerSecond: rate,
		ChargedAt: chargedAt,
	})
}

func settledBillingResultTx(ctx context.Context, tx *storeTx, a billingAward) (capability.BillingSettlement, error) {
	result := capability.BillingSettlement{ReservationID: a.provenance.ReservationID}
	if !a.provenance.Metered {
		return result, nil
	}
	var seconds int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(seconds),0) FROM credit_charges WHERE team=? AND capability_prefix=? AND run_id=? AND node_id=? AND kind IN ('reservation','slice','usage','refund')", a.team, a.prefix, a.run, a.node).Scan(&seconds); err != nil {
		return result, err
	}
	result.BillableSeconds = seconds
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM billable_intervals WHERE team=? AND reservation_id=?", a.team, a.provenance.ReservationID).Scan(&count); err != nil {
		return result, err
	}
	result.RefundUnstarted = count == 0
	return result, nil
}
