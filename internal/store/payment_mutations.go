package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func reversePaymentTx(ctx context.Context, tx *storeTx, paymentID, reference string, actor auditActor, amountMicro int64) (creditReversal, error) {
	paymentID, reference = strings.TrimSpace(paymentID), strings.TrimSpace(reference)
	createdBy := actor.subject.ID
	if actor.system != "" {
		createdBy = string(actor.system)
	}
	if paymentID == "" || reference == "" {
		return creditReversal{}, errors.New("credits: a reversal names the payment and its own reference")
	}
	paid, found, err := tx.operator.paidGrantForPaymentTx(ctx, tx, paymentID)
	if err != nil {
		return creditReversal{}, err
	}
	if !found {
		return creditReversal{}, fmt.Errorf("%w: %q", errUnknownPayment, paymentID)
	}
	l, err := lockLedgerTx(ctx, tx, paid.Team)
	if err != nil {
		return creditReversal{}, err
	}
	if _, err := refuseBoundDisputeTx(ctx, tx, reference, paid.Team, paymentID); err != nil {
		return creditReversal{}, err
	}
	out := creditReversal{Team: paid.Team, PaidMicro: paid.AmountMicro}
	existing, found, err := creditGrantByReferenceTx(ctx, tx, paid.Team, creditGrantReversal, reference)
	if err != nil {
		return creditReversal{}, err
	}
	if found {
		if existing.Reverses != paid.Reference || existing.ReversesKind != CreditGrantPaid {
			return creditReversal{}, fmt.Errorf("%w: %q reverses %q, not %q",
				ErrCreditGrantConflict, reference, existing.Reverses, paid.Reference)
		}
		out.Grant = &existing
	}
	already, err := reversedMicroTx(ctx, tx, paid.Team, CreditGrantPaid, paid.Reference)
	if err != nil {
		return creditReversal{}, err
	}
	if out.Grant == nil && already < paid.AmountMicro {
		id, err := newCreditID("grant")
		if err != nil {
			return creditReversal{}, err
		}
		take := paid.AmountMicro - already
		if amountMicro > 0 && amountMicro < take {
			take = amountMicro
		}
		grant := CreditGrant{
			ID: id, Team: paid.Team, Kind: creditGrantReversal, AmountMicro: -take,
			Reference: reference, Reverses: paid.Reference, ReversesKind: CreditGrantPaid, CreatedBy: createdBy, CreatedAt: tx.now(ctx),
		}
		if err := l.insertGrant(ctx, grant); err != nil {
			return creditReversal{}, err
		}
		if err := recordGrantEventTx(ctx, tx, grant, actor, auditGrant{}); err != nil {
			return creditReversal{}, err
		}
		out.Grant, out.Created = &grant, true
		already += take
	}
	out.ReversedMicro = already
	if out.BalanceMicro, err = l.balance(ctx); err != nil {
		return creditReversal{}, err
	}
	return out, nil
}

func holdCreditFreezeTx(ctx context.Context, tx *storeTx, team Team, disputeID, paymentID, reason, actor string, now time.Time, provenance auditActor) (bool, error) {
	disputeID, paymentID = strings.TrimSpace(disputeID), strings.TrimSpace(paymentID)
	if disputeID == "" {
		return false, errors.New("credits: a hold names the dispute it is for")
	}
	// safety: admission and holds serialize on the team's ledger lock.
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return false, err
	}
	var registered int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM teams WHERE name = ?`, string(team)).Scan(&registered); err != nil {
		return false, err
	}
	if registered == 0 {
		return false, fmt.Errorf("%w: %s", ErrUnknownTeam, team)
	}
	bound, err := refuseBoundDisputeTx(ctx, tx, disputeID, team, paymentID)
	if err != nil || bound {
		return false, err
	}
	stored, cause := reason, HoldCauseOperator
	if actor == "" {
		stored, cause = truncate(reason, 500), holdCauseDispute
	}
	mode, err := creditPaymentMode(ctx, tx)
	if err != nil {
		return false, err
	}
	if _, err := tx.execContext(ctx, `
		INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, cause, placed_by, created_at, payment_mode) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		disputeID, string(team), paymentID, stored, cause, actor, now.UnixNano(), mode); err != nil {
		return false, err
	}
	if err := tx.operator.recordAudit(ctx, tx, auditContext{At: now, Team: team, Actor: provenance}, auditHold{HoldID: digestAudit(disputeID), PaymentID: digestAudit(paymentID)}); err != nil {
		return false, err
	}
	return true, nil
}

func releaseCreditFreezesTx(ctx context.Context, tx *storeTx, team Team, disputeID string, operatorOnly, noFault bool, actor, reason string, now time.Time, provenance auditActor) (int64, error) {
	if team == "" {
		return 0, errors.New("credits: a release names the team")
	}
	disputeID = strings.TrimSpace(disputeID)
	operatorOnly = operatorOnly || noFault
	mode, err := creditPaymentMode(ctx, tx)
	if err != nil {
		return 0, err
	}
	query := `UPDATE credit_freezes SET released_at = ?, no_fault = ? WHERE team = ? AND payment_mode = ? AND released_at IS NULL`
	args := []any{now.UnixNano(), boolInt(noFault), string(team), mode}
	if disputeID != "" {
		query, args = query+` AND dispute_id = ?`, append(args, disputeID)
	}
	if operatorOnly {
		query, args = query+` AND cause = ?`, append(args, HoldCauseOperator)
	}
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return 0, err
	}
	if noFault {
		var operator, others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN placed_by <> ? THEN 1 ELSE 0 END), 0)
		    FROM credit_freezes WHERE team = ? AND payment_mode = ? AND released_at IS NULL AND cause = ? AND (? = '' OR dispute_id = ?)`,
			actor, string(team), mode, HoldCauseOperator, disputeID, disputeID).Scan(&operator, &others); err != nil {
			return 0, err
		}
		if disputeID != "" && operator == 0 {
			return 0, fmt.Errorf("%w: only an operator hold can be released as no fault found", ErrInvalidInput)
		}
		if actor == "" || others > 0 {
			return 0, authorityError(apierr.Forbidden, "only the operator who placed a hold can release it as no fault found")
		}
	}
	released, err := releasedDisputesTx(ctx, tx, query+` RETURNING dispute_id`, args...)
	if err != nil {
		return 0, err
	}
	for _, id := range released {
		if err := tx.operator.recordAudit(ctx, tx, auditContext{At: now, Team: team, Actor: provenance}, auditHold{HoldID: digestAudit(id), Released: true, NoFault: noFault}); err != nil {
			return 0, err
		}
	}
	return int64(len(released)), nil
}

// saveCardTx records that the setup gave the team's customer a card; setup names the setup intent for the audit.
func saveCardTx(ctx context.Context, tx *storeTx, team Team, c Card, setup string, actor auditActor, now time.Time) error {
	if c.Customer == "" || setup == "" {
		return fmt.Errorf("%w: a card setup names its customer and setup", ErrInvalidInput)
	}
	if err := lockPaymentTeamTx(ctx, tx, team); err != nil {
		return err
	}
	if _, err := lockLedgerTx(ctx, tx, team); err != nil {
		return err
	}
	addedAt := now.UnixNano()
	if !c.AddedAt.IsZero() {
		addedAt = c.AddedAt.UnixNano()
	}
	res, err := tx.execContext(ctx, `UPDATE teams SET card_customer = ?, card_added_at = ?
	    WHERE name = ? AND card_added_at <= ?`, c.Customer, addedAt, string(team), addedAt)
	if err != nil {
		return err
	}
	saved, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if saved == 0 {
		return errStaleCardSetup
	}
	if _, err := tx.execContext(ctx, `UPDATE credit_checkouts SET refill_blocked = 0
	    WHERE team = ? AND refill_blocked = 1 AND opened_at < ?`, string(team), addedAt); err != nil {
		return err
	}
	if err := tx.operator.recordAudit(ctx, tx, auditContext{Team: team, Actor: actor, At: now}, auditCard{Reference: digestAudit(setup), Operation: businessEventCardAdded}); err != nil {
		return err
	}
	return nil
}

// applyCardRefundTx records the outcome of the payment's pending refund. Failed and canceled are terminal: Stripe
// declined to return the money, so the operator settles it by hand rather than the worker retrying.
func applyCardRefundTx(ctx context.Context, tx *storeTx, team Team, paymentIntent, refundID, status string) error {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return err
	}
	switch status {
	case cardRefundSucceeded, "failed", "canceled":
	default:
		status = cardRefundPending
	}
	_, err = tx.execContext(ctx, `UPDATE card_refunds SET status = ? WHERE payment_mode=? AND team = ? AND payment_intent = ?
	    AND refund_id = ? AND status = 'pending'`, status, mode, string(team), paymentIntent, refundID)
	return err
}
