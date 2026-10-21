package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func reversePaymentTx(ctx context.Context, tx *storeTx, paymentID, reference, createdBy string, amountMicro int64) (CreditReversal, error) {
	paymentID, reference = strings.TrimSpace(paymentID), strings.TrimSpace(reference)
	if paymentID == "" || reference == "" {
		return CreditReversal{}, errors.New("credits: a reversal names the payment and its own reference")
	}
	paid, found, err := tx.operator.paidGrantForPaymentTx(ctx, tx, paymentID)
	if err != nil {
		return CreditReversal{}, err
	}
	if !found {
		return CreditReversal{}, fmt.Errorf("%w: %q", ErrUnknownPayment, paymentID)
	}
	l, err := lockLedgerTx(ctx, tx, paid.Team)
	if err != nil {
		return CreditReversal{}, err
	}
	if _, err := refuseBoundDisputeTx(ctx, tx, reference, paid.Team, paymentID); err != nil {
		return CreditReversal{}, err
	}
	out := CreditReversal{Team: paid.Team, PaidMicro: paid.AmountMicro}
	existing, found, err := creditGrantByReferenceTx(ctx, tx, paid.Team, CreditGrantReversal, reference)
	if err != nil {
		return CreditReversal{}, err
	}
	if found {
		if existing.Reverses != paid.Reference {
			return CreditReversal{}, fmt.Errorf("%w: %q reverses %q, not %q",
				ErrCreditGrantConflict, reference, existing.Reverses, paid.Reference)
		}
		out.Grant = &existing
	}
	already, err := reversedMicroTx(ctx, tx, paid.Team, paid.Reference)
	if err != nil {
		return CreditReversal{}, err
	}
	if out.Grant == nil && already < paid.AmountMicro {
		id, err := newCreditID("grant")
		if err != nil {
			return CreditReversal{}, err
		}
		take := paid.AmountMicro - already
		if amountMicro > 0 && amountMicro < take {
			take = amountMicro
		}
		clockTime1, clockErr1 := tx.now(ctx)
		if clockErr1 != nil {
			return CreditReversal{}, clockErr1
		}
		grant := CreditGrant{
			ID: id, Team: paid.Team, Kind: CreditGrantReversal, AmountMicro: -take,
			Reference: reference, Reverses: paid.Reference, CreatedBy: createdBy, CreatedAt: clockTime1,
		}
		if err := l.insertGrant(ctx, grant); err != nil {
			return CreditReversal{}, err
		}
		if err := recordGrantEventTx(tx, grant, nil); err != nil {
			return CreditReversal{}, err
		}
		out.Grant, out.Created = &grant, true
		already += take
	}
	out.ReversedMicro = already
	if out.BalanceMicro, err = l.balance(ctx); err != nil {
		return CreditReversal{}, err
	}
	return out, nil
}

func holdCreditFreezeTx(ctx context.Context, tx *storeTx, team Team, disputeID, paymentID, reason, actor string, now time.Time) (bool, error) {
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
		stored, cause = truncate(reason, 500), HoldCauseDispute
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, cause, placed_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		disputeID, string(team), paymentID, stored, cause, actor, now.UnixNano()); err != nil {
		return false, err
	}
	if err := stopHeldAwardsTx(ctx, tx, team); err != nil {
		return false, err
	}
	attrs := map[string]any{"payment_id": paymentID}
	// safety: provider dispute reasons are untrusted free text and never enter audit events.
	if actor != "" {
		attrs["reason"] = reason
	}
	if err := tx.operator.RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: team, Kind: BusinessEventTeamFrozen, SubjectID: disputeID, Actor: actor, Attrs: attrs,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func releaseCreditFreezesTx(ctx context.Context, tx *storeTx, team Team, disputeID string, operatorOnly, noFault bool, actor, reason string, now time.Time) (int64, error) {
	if team == "" {
		return 0, errors.New("credits: a release names the team")
	}
	disputeID = strings.TrimSpace(disputeID)
	operatorOnly = operatorOnly || noFault
	query := `UPDATE credit_freezes SET released_at = ?, no_fault = ? WHERE team = ? AND released_at IS NULL`
	args := []any{now.UnixNano(), boolInt(noFault), string(team)}
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
		    FROM credit_freezes WHERE team = ? AND released_at IS NULL AND cause = ? AND (? = '' OR dispute_id = ?)`,
			actor, string(team), HoldCauseOperator, disputeID, disputeID).Scan(&operator, &others); err != nil {
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
	var releaseAttrs map[string]any
	if actor != "" {
		releaseAttrs = map[string]any{"reason": reason}
		if noFault {
			releaseAttrs["no_fault"] = true
		}
	}
	for _, id := range released {
		if err := tx.operator.RecordBusinessEvent(tx, BusinessEvent{
			At: now, Team: team, Kind: BusinessEventTeamUnfrozen, SubjectID: id, Actor: actor, Attrs: releaseAttrs,
		}); err != nil {
			return 0, err
		}
	}
	return int64(len(released)), nil
}

func saveCardTx(ctx context.Context, tx *storeTx, team Team, c Card, actor string, now time.Time) error {
	if c.Customer == "" || c.PaymentMethod == "" || c.Fingerprint == "" {
		return fmt.Errorf("%w: a card names its customer, payment method and fingerprint", ErrInvalidInput)
	}
	if err := (&Tenant{s: tx.store, team: team}).lockTeamTx(ctx, tx); err != nil {
		return err
	}
	refusal, err := cardRefusalTx(ctx, tx, team, "", c.Fingerprint)
	if err != nil {
		return err
	}
	if refusal.reason != "" {
		return tx.operator.RecordBusinessEvent(tx, BusinessEvent{
			Team: team, Kind: BusinessEventCardRefused, SubjectID: c.PaymentMethod, Actor: actor, At: now,
			Attrs: map[string]any{"reason": refusal.reason, "brand": c.Brand, "last4": c.Last4},
		})
	}
	addedAt := now.UnixNano()
	if !c.AddedAt.IsZero() {
		addedAt = c.AddedAt.UnixNano()
	}
	res, err := tx.ExecContext(ctx, `UPDATE teams SET card_customer = ?, card_payment_method = ?,
	    card_fingerprint = ?, card_brand = ?, card_last4 = ?, card_added_at = ?
	    WHERE name = ? AND card_added_at <= ?`,
		c.Customer, c.PaymentMethod, c.Fingerprint, c.Brand, c.Last4, addedAt, string(team), addedAt)
	if err != nil {
		return err
	}
	saved, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if saved == 0 {
		return ErrStaleCardSetup
	}
	if _, err := tx.ExecContext(ctx, `UPDATE credit_checkouts SET refill_blocked = 0
	    WHERE team = ? AND refill_blocked = 1 AND payment_method <> ?`, string(team), c.PaymentMethod); err != nil {
		return err
	}
	if err := tx.operator.RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardAdded, SubjectID: c.PaymentMethod, Actor: actor, At: now,
		Attrs: map[string]any{"brand": c.Brand, "last4": c.Last4},
	}); err != nil {
		return err
	}
	return nil
}

func applyCardRefundTx(ctx context.Context, tx *storeTx, team Team, paymentIntent, refundID, status, when string, whenArgs []any, now time.Time) (bool, error) {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return false, err
	}
	var attempts int64
	args := append([]any{mode, team, paymentIntent}, whenArgs...)
	err = tx.QueryRowContext(ctx, `SELECT attempts FROM card_refunds WHERE payment_mode=? AND team = ? AND payment_intent = ? AND `+when+tx.forUpdate(), args...).Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if attempts < 0 {
		return false, fmt.Errorf("%w: refund attempts must be nonnegative", ErrInvalidInput)
	}
	set, args := `refund_id = ?, status = ?`, []any{refundID, CardRefundPending}
	switch status {
	case CardRefundSucceeded:
		args[1] = CardRefundSucceeded
	case "failed", "canceled":
		set = `refund_id = ?, status = ?, attempts = attempts + 1, next_attempt_at = ?`
		delay := cardRefundRetryAfter[min(attempts, int64(len(cardRefundRetryAfter)-1))]
		args = []any{refundID, CardRefundDue, now.Add(delay).UnixNano()}
	}
	args = append(append(args, mode, string(team), paymentIntent), whenArgs...)
	res, err := tx.ExecContext(ctx, `UPDATE card_refunds SET `+set+` WHERE payment_mode=? AND team = ? AND payment_intent = ? AND `+when, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
