package store

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// CreditLedgerTotals reports deployment-wide financial totals from one snapshot.
func (o *Operator) CreditLedgerTotals(ctx context.Context) (CreditLedgerTotals, error) {
	calculate := o.s.billing
	if calculate == nil {
		calculate = capability.SettleBilling
	}
	return o.s.creditLedgerTotalsWith(ctx, calculate)
}

func (s *Store) creditLedgerTotalsWith(ctx context.Context, calculate billingCalculator) (_ CreditLedgerTotals, err error) {
	var out CreditLedgerTotals
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return CreditLedgerTotals{}, err
	}
	defer rollbackUnlessDone(tx, &err)

	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        -COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0)
                   FROM credit_grants`,
		CreditGrantFree, CreditGrantPaid, CreditGrantReversal,
	).Scan(&out.GrantedFreeMicro, &out.GrantedPaidMicro, &out.ReversedMicro); err != nil {
		return CreditLedgerTotals{}, err
	}
	var settled, charged int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN -amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(amount_micro), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN 0 ELSE seconds END), 0)
                   FROM credit_charges`,
		CreditChargeReservation, CreditChargeUsage, CreditChargeRefund,
		CreditChargeStorage, CreditChargeStorage,
	).Scan(&out.ReservedMicro, &out.ChargedMicro, &out.RefundedMicro, &out.StorageMicro,
		&settled, &charged); err != nil {
		return CreditLedgerTotals{}, err
	}

	refundable, err := refundableBillingSecondsTx(ctx, tx, calculate)
	if err != nil {
		return CreditLedgerTotals{}, err
	}
	out.SettledSeconds = charged - refundable

	out.BalanceMicro = out.GrantedFreeMicro + out.GrantedPaidMicro - out.ReversedMicro - settled
	if err := tx.Commit(); err != nil {
		return CreditLedgerTotals{}, err
	}
	return out, nil
}

type pendingBillingSeconds struct {
	award billingAward
	paid  int64
}

func refundableBillingSecondsTx(ctx context.Context, tx *storeTx, calculate billingCalculator) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.team,a.reservation_id,a.capability_prefix,a.run_id,a.node_id,
	 a.payer_team,a.executor_class,a.billing_start,a.billing_unit,
	 (SELECT COALESCE(SUM(c.seconds),0) FROM credit_charges c WHERE c.team=a.team
	 AND c.capability_prefix=a.capability_prefix AND c.run_id=a.run_id AND c.node_id=a.node_id
	 AND c.kind IN ('reservation','usage','refund'))
	 FROM billing_awards a WHERE a.metered=1 AND a.settled_at IS NULL`)
	if err != nil {
		return 0, err
	}
	var pending []pendingBillingSeconds
	for rows.Next() {
		var p pendingBillingSeconds
		a := &p.award
		a.provenance.Metered = true
		if err := rows.Scan(&a.team, &a.provenance.ReservationID, &a.prefix, &a.run, &a.node,
			&a.provenance.PayerTeam, &a.provenance.ExecutorClass, &a.provenance.Start, &a.provenance.Unit, &p.paid); err != nil {
			return 0, errors.Join(err, rows.Close())
		}
		pending = append(pending, p)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	var refundable int64
	through := tx.now()
	for _, p := range pending {
		// safety: unconfirmed time may be refunded by a later settlement at the persisted lease lapse.
		intervals, err := readBillingIntervalsTx(ctx, tx, p.award, through, true)
		if err != nil {
			return 0, err
		}
		result, err := calculate(p.award.provenance, intervals)
		if err != nil {
			return 0, err
		}
		if err := validateBillingCalculation(p.award, intervals, result); err != nil {
			return 0, err
		}
		refundable += max(p.paid-result.BillableSeconds, 0)
	}
	return refundable, nil
}
