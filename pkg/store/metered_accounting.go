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
	team                          Team
	provenance                    capability.BillingProvenance
	prefix, run, node             string
	unit                          capability.UnitKind
	generation, awarded, reserved int64
	settled                       sql.NullInt64
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
	err := tx.QueryRowContext(ctx, `SELECT capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,reserved_amount_micro,settled_at FROM billing_awards WHERE team=? AND reservation_id=?`+tx.forUpdate(), team, reservation).Scan(&a.prefix, &a.run, &a.node, &a.generation, &a.provenance.PayerTeam, &a.provenance.Metered, &a.provenance.ExecutorClass, &a.provenance.Start, &a.unit, &a.provenance.Unit, &a.awarded, &a.reserved, &a.settled)
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
	for _, m := range capability.UnitBillingMappings {
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
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return err
	}
	existing, err := readBillingAwardTx(ctx, tx, Team(c.Team), b.ReservationID)
	if err == nil {
		if existing.provenance != *b || existing.prefix != c.Prefix || existing.run != c.Binding.RunID || existing.node != c.Binding.NodeID || existing.generation != generation || existing.unit != c.Unit || existing.reserved != amount {
			return authorityError(apierr.Conflict, "billing award is immutable")
		}
		if b.Metered {
			rate, class, seconds, _, err := billingPriceTx(ctx, tx, existing)
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
		balance, err := creditBalanceTx(ctx, tx, Team(c.Team))
		if err != nil {
			return err
		}
		if err := refuseFrozenTeamTx(ctx, tx, Team(c.Team), balance, amount, c.Binding.RunID, c.Binding.NodeID); err != nil {
			return err
		}
		if err := refuseAboveHeadroomTx(ctx, tx, Team(c.Team), amount, tx.now(), c.Binding.RunID, c.Binding.NodeID); err != nil {
			return err
		}
	}
	now := tx.now().UnixNano()
	_, err = tx.ExecContext(ctx, `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,reserved_amount_micro) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.Team, b.ReservationID, c.Prefix, c.Binding.RunID, c.Binding.NodeID, generation, b.PayerTeam, boolInt(b.Metered), b.ExecutorClass, b.Start, c.Unit, b.Unit, now, amount)
	if err != nil {
		return err
	}
	if b.Metered {
		fingerprint, err := teamCardTx(ctx, tx, Team(c.Team))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,principal,cpu_class,rate_micro_per_second,charged_at,card_fingerprint) VALUES (?,?,?,?,?,'reservation',?,?,?,?,?,?,?)`, billingID("reservation", Team(c.Team), b.ReservationID, ""), c.Team, c.Binding.RunID, c.Binding.NodeID, c.Prefix, reserveSeconds, amount, c.Subject.ID, price.Cores, price.MicroPerSecond, now, fingerprint)
		if err != nil {
			return err
		}
		if err := addSpendTx(ctx, tx, Team(c.Team), fingerprint, now, amount); err != nil {
			return err
		}
	}
	if b.Start == capability.BillingAtAward {
		return startBillableIntervalTx(ctx, tx, Team(c.Team), b.ReservationID, "award", 0, tx.now())
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
	if at.After(tx.now()) || at.UnixNano() < a.awarded {
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
	_, err = tx.ExecContext(ctx, `INSERT INTO billable_intervals(team,id,run_id,node_id,attempt,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,started_at,charged_through) VALUES (?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?)`, team, id, a.run, a.node, attempt, a.generation, a.prefix, reservation, a.provenance.PayerTeam, a.provenance.ExecutorClass, a.unit, a.provenance.Unit, a.provenance.Start, at.UnixNano(), at.UnixNano())
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
	var start int64
	var end sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT started_at,finished_at FROM billable_intervals WHERE team=? AND reservation_id=? AND id=?", team, reservation, id).Scan(&start, &end); err != nil {
		return err
	}
	if at.After(tx.now()) || at.UnixNano() < start {
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
	_, err = tx.ExecContext(ctx, "UPDATE billable_intervals SET finished_at=? WHERE team=? AND reservation_id=? AND id=?", at.UnixNano(), team, reservation, id)
	return err
}

type billingCalculator func(capability.BillingProvenance, []capability.BillableInterval) (capability.BillingSettlement, error)

// safety: transitions pass the persisted lease lapse on disruption, never the later reaper clock.
// Calculation reads durable awards and intervals even after the claimant or run has been pruned.
func settleBillingAwardTx(ctx context.Context, tx *storeTx, team Team, reservation string, through time.Time) (capability.BillingSettlement, error) {
	return settleBillingAwardWithTx(ctx, tx, team, reservation, through, capability.SettleBilling)
}

func settleBillingAwardWithTx(ctx context.Context, tx *storeTx, team Team, reservation string, through time.Time, calculate billingCalculator) (capability.BillingSettlement, error) {
	return accountBillingAwardTx(ctx, tx, team, reservation, through, true, calculate)
}

// safety: renewal charges elapsed compute in its lease transaction without closing the interval.
func chargeBillingAwardTx(ctx context.Context, tx *storeTx, team Team, reservation string, through time.Time) (capability.BillingSettlement, error) {
	return accountBillingAwardTx(ctx, tx, team, reservation, through, false, capability.SettleBilling)
}

func accountBillingAwardTx(ctx context.Context, tx *storeTx, team Team, reservation string, through time.Time, final bool, calculate billingCalculator) (capability.BillingSettlement, error) {
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return capability.BillingSettlement{}, err
	}
	a, err := readBillingAwardTx(ctx, tx, team, reservation)
	if err != nil {
		return capability.BillingSettlement{}, err
	}
	if a.settled.Valid {
		return settledBillingResultTx(ctx, tx, a)
	}
	if through.After(tx.now()) || through.UnixNano() < a.awarded {
		return capability.BillingSettlement{}, authorityError(apierr.BadRequest, "billing checkpoint is outside its awarded time range")
	}
	intervals, err := billingIntervalsTx(ctx, tx, a, through, final)
	if err != nil {
		return capability.BillingSettlement{}, err
	}
	result, err := calculate(a.provenance, intervals)
	if err != nil {
		return result, err
	}
	if result.ReservationID != reservation || result.BillableSeconds < 0 || (result.RefundUnstarted && (len(intervals) > 0 || result.BillableSeconds != 0)) || (!a.provenance.Metered && result.BillableSeconds != 0) {
		return result, authorityError(apierr.Conflict, "invalid billing calculation")
	}
	if a.provenance.Metered {
		if err := settleBillingChargesTx(ctx, tx, a, result, through, final); err != nil {
			return result, err
		}
	}
	if !final {
		return result, nil
	}
	_, err = tx.ExecContext(ctx, "UPDATE billing_awards SET settled_at=? WHERE team=? AND reservation_id=? AND settled_at IS NULL", tx.now().UnixNano(), team, reservation)
	return result, err
}

func billingIntervalsTx(ctx context.Context, tx *storeTx, a billingAward, through time.Time, final bool) ([]capability.BillableInterval, error) {
	rows, err := tx.QueryContext(ctx, "SELECT capability_prefix,billing_unit,started_at,finished_at,charged_through FROM billable_intervals WHERE team=? AND reservation_id=? ORDER BY started_at,id", a.team, a.provenance.ReservationID)
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
		}
		if charged > through.UnixNano() || i.End.Before(i.Start) || i.End.After(through) || i.CapabilityPrefix != a.prefix || i.Unit != a.provenance.Unit {
			return nil, errors.Join(authorityError(apierr.Conflict, "persisted interval conflicts with settlement"), rows.Close())
		}
		intervals = append(intervals, i)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if final {
		_, err = tx.ExecContext(ctx, "UPDATE billable_intervals SET finished_at=COALESCE(finished_at,?),charged_through=COALESCE(finished_at,?) WHERE team=? AND reservation_id=?", through.UnixNano(), through.UnixNano(), a.team, a.provenance.ReservationID)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE billable_intervals SET charged_through=COALESCE(finished_at,?) WHERE team=? AND reservation_id=?", through.UnixNano(), a.team, a.provenance.ReservationID)
	}
	return intervals, err
}

func billingPriceTx(ctx context.Context, tx *storeTx, a billingAward) (rate, class, seconds int64, fingerprint string, err error) {
	err = tx.QueryRowContext(ctx, "SELECT rate_micro_per_second,cpu_class,seconds,card_fingerprint FROM credit_charges WHERE team=? AND id=? AND kind='reservation'", a.team, billingID("reservation", a.team, a.provenance.ReservationID, "")).Scan(&rate, &class, &seconds, &fingerprint)
	return rate, class, seconds, fingerprint, err
}

func settleBillingChargesTx(ctx context.Context, tx *storeTx, a billingAward, result capability.BillingSettlement, through time.Time, final bool) error {
	rate, class, _, fingerprint, err := billingPriceTx(ctx, tx, a)
	if err != nil {
		return err
	}
	total, err := billingProduct(result.BillableSeconds, rate)
	if err != nil {
		return err
	}
	var paid, paidSeconds int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount_micro),0),COALESCE(SUM(seconds),0) FROM credit_charges WHERE team=? AND capability_prefix=? AND run_id=? AND node_id=? AND kind IN ('reservation','usage','refund')", a.team, a.prefix, a.run, a.node).Scan(&paid, &paidSeconds); err != nil {
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
	kind := "usage"
	chargedAt := tx.now().UnixNano()
	if delta < 0 {
		kind = "refund"
		chargedAt = a.awarded
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,cpu_class,rate_micro_per_second,charged_at,card_fingerprint) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, id, a.team, a.run, a.node, a.prefix, kind, result.BillableSeconds-paidSeconds, delta, class, rate, chargedAt, fingerprint)
	if err != nil {
		return err
	}
	return addSpendTx(ctx, tx, a.team, fingerprint, chargedAt, delta)
}

func settledBillingResultTx(ctx context.Context, tx *storeTx, a billingAward) (capability.BillingSettlement, error) {
	result := capability.BillingSettlement{ReservationID: a.provenance.ReservationID}
	if !a.provenance.Metered {
		return result, nil
	}
	var seconds int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(seconds),0) FROM credit_charges WHERE team=? AND capability_prefix=? AND run_id=? AND node_id=? AND kind IN ('reservation','usage','refund')", a.team, a.prefix, a.run, a.node).Scan(&seconds); err != nil {
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
