package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func reversePaymentTx(ctx context.Context, tx *storeTx, paymentID, reference, createdBy string, amountMicro int64) (CreditReversal, error) {
	paymentID, reference = strings.TrimSpace(paymentID), strings.TrimSpace(reference)
	if paymentID == "" || reference == "" {
		return CreditReversal{}, errors.New("credits: a reversal names the payment and its own reference")
	}
	if strings.HasPrefix(reference, unappliedPrefix) {
		return CreditReversal{}, fmt.Errorf("%w: the reference prefix %q is reserved", ErrInvalidInput, unappliedPrefix)
	}
	id, err := newCreditID("grant")
	if err != nil {
		return CreditReversal{}, err
	}
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return CreditReversal{}, err
	}
	paid, found, err := paidGrantForPaymentTx(ctx, tx, paymentID)
	if err != nil {
		return CreditReversal{}, err
	}
	if !found {
		return CreditReversal{}, fmt.Errorf("%w: %q", ErrUnknownPayment, paymentID)
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
	if out.Grant == nil && amountMicro > 0 {
		released, err := releaseUnappliedTx(ctx, tx, paid.Team, paid.Reference, reference, createdBy, amountMicro)
		if err != nil {
			return CreditReversal{}, err
		}
		already -= released
	}
	if out.Grant == nil && already < paid.AmountMicro {
		now := tx.now()
		take := paid.AmountMicro - already
		if amountMicro > 0 && amountMicro < take {
			take = amountMicro
		}
		grant := CreditGrant{
			ID: id, Team: paid.Team, Kind: CreditGrantReversal, AmountMicro: -take,
			Reference: reference, Reverses: paid.Reference, CreatedBy: createdBy, CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `
        INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			string(grant.Team), grant.ID, grant.Kind, grant.AmountMicro, grant.Reference, grant.Reverses,
			grant.CreatedBy, now.UnixNano()); err != nil {
			return CreditReversal{}, fmt.Errorf("credits: insert reversal: %w", err)
		}
		if err := recordGrantEventTx(tx, grant, "", ""); err != nil {
			return CreditReversal{}, err
		}
		out.Grant, out.Created = &grant, true
		already += take
	}
	out.ReversedMicro = already
	if out.BalanceMicro, err = creditBalanceTx(ctx, tx, paid.Team); err != nil {
		return CreditReversal{}, err
	}
	return out, nil
}

func holdCreditFreezeTx(ctx context.Context, tx *storeTx, team Team, disputeID, paymentID, reason, actor string, now time.Time) (bool, error) {
	disputeID, paymentID = strings.TrimSpace(disputeID), strings.TrimSpace(paymentID)
	if disputeID == "" {
		return false, errors.New("credits: a hold names the dispute it is for")
	}
	// safety: admission and freezes serialize on the same ledger lock.
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
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
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, created_at) VALUES (?, ?, ?, ?, ?)`,
		disputeID, string(team), paymentID, truncate(reason, 500), now.UnixNano()); err != nil {
		return false, err
	}
	attrs := map[string]any{"payment_id": paymentID}
	// safety: provider dispute reasons are untrusted free text and never enter audit events.
	if actor != "" {
		attrs["reason"] = reason
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: team, Kind: BusinessEventTeamFrozen, SubjectID: disputeID, Actor: actor, Attrs: attrs,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func releaseCreditFreezesTx(ctx context.Context, tx *storeTx, team Team, disputeID string, operatorOnly bool, actor, reason string, now time.Time) (int64, error) {
	if team == "" {
		return 0, errors.New("credits: a release names the team")
	}
	query := `UPDATE credit_freezes SET released_at = ? WHERE team = ? AND released_at IS NULL`
	args := []any{now.UnixNano(), string(team)}
	if disputeID = strings.TrimSpace(disputeID); disputeID != "" {
		query, args = query+` AND dispute_id = ?`, append(args, disputeID)
	}
	if operatorOnly {
		query, args = query+` AND dispute_id LIKE ?`, append(args, OperatorHoldPrefix+"%")
	}
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return 0, err
	}
	released, err := releasedDisputesTx(ctx, tx, query+` RETURNING dispute_id`, args...)
	if err != nil {
		return 0, err
	}
	var releaseAttrs map[string]any
	if actor != "" {
		releaseAttrs = map[string]any{"reason": reason}
	}
	for _, id := range released {
		if err := RecordBusinessEvent(tx, BusinessEvent{
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
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return err
	}
	if err := (&Tenant{s: tx.store, team: team}).lockTeamTx(ctx, tx); err != nil {
		return err
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
	if _, err := tx.ExecContext(ctx, `UPDATE card_charges SET next_attempt_at = ? WHERE team = ? AND state = ?`,
		now.UnixNano(), string(team), CardChargeOpen); err != nil {
		return err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardAdded, SubjectID: c.PaymentMethod, Actor: actor, At: now,
		Attrs: map[string]any{"brand": c.Brand, "last4": c.Last4},
	}); err != nil {
		return err
	}
	return nil
}

func settleCardPaymentTx(ctx context.Context, tx *storeTx, p CardPayment, now time.Time) (created bool, warned *WarnedCardPayment, err error) {
	if p.PaymentIntent == "" || p.AmountCents <= 0 || p.ChargeID == "" || p.AttemptID == "" {
		return false, nil, fmt.Errorf("%w: a card payment names its charge, attempt, payment intent and amount", ErrInvalidInput)
	}
	amount, err := billingProduct(p.AmountCents, MicroCreditsPerCent)
	if err != nil {
		return false, nil, err
	}
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return false, nil, err
	}
	var a cardAttemptRow
	err = tx.QueryRowContext(ctx, `SELECT team, charge_id, kind, status, payment_intent FROM card_attempts WHERE id = ?`,
		p.AttemptID).Scan(&a.team, &a.chargeID, &a.kind, &a.status, &a.paymentIntent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, fmt.Errorf("%w: card attempt %q", ErrNotFound, p.AttemptID)
	}
	if err != nil {
		return false, nil, err
	}
	if a.chargeID != p.ChargeID || (p.Team != "" && Team(a.team) != p.Team) {
		return false, nil, fmt.Errorf("%w: card attempt %q is not for charge %q of this team", ErrInvalidInput, p.AttemptID, p.ChargeID)
	}
	team := Team(a.team)
	if done, err := cardPaymentSeenTx(ctx, tx, team, p.PaymentIntent); err != nil || done {
		return false, nil, err
	}
	var state string
	var frozenAmount int64
	if err := tx.QueryRowContext(ctx, `SELECT state, amount_micro FROM card_charges WHERE team = ? AND id = ?`+tx.forUpdate(), team, p.ChargeID).
		Scan(&state, &frozenAmount); err != nil {
		return false, nil, err
	}
	nowNS := now.UnixNano()
	payable := state == CardChargeOpen && a.status == CardAttemptLive && amount == frozenAmount &&
		(a.paymentIntent == "" || a.paymentIntent == p.PaymentIntent)
	warning, err := paymentWarningTx(ctx, tx, p.PaymentIntent, p.Fingerprint)
	if err != nil {
		return false, nil, err
	}
	if warning != "" {
		repays, repaid, err := warnedCardRepaidTx(ctx, tx, team, a.kind, p.PaymentIntent, payable, amount)
		if err != nil {
			return false, nil, err
		}
		warned = &WarnedCardPayment{WarningID: warning, Repays: repays, RepaidMicro: repaid, UnappliedMicro: amount - repaid}
		if err := settleWarnedTx(ctx, tx, team, p, *warned, now); err != nil {
			return false, nil, err
		}
		if !repays {
			return false, warned, nil
		}
	}
	if err := insertCardGrantTx(ctx, tx, team, amount, p.PaymentIntent, nowNS); err != nil {
		return false, nil, err
	}
	if warned != nil && warned.UnappliedMicro > 0 {
		if err := insertUnappliedTx(ctx, tx, team, p.PaymentIntent, warned.UnappliedMicro, nowNS); err != nil {
			return false, nil, err
		}
	}
	if !payable {
		reason := "duplicate"
		if amount != frozenAmount {
			reason = "amount_mismatch"
		}
		if err := queueCardRefundTx(ctx, tx, team, p, reason, now); err != nil {
			return false, nil, err
		}
		return true, nil, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, payment_intent = ?, updated_at = ?
     WHERE team = ? AND id = ? AND charge_id = ? AND status = ?`, CardAttemptPaid, p.PaymentIntent, nowNS, team, p.AttemptID, p.ChargeID, CardAttemptLive)
	if err != nil {
		return false, nil, err
	}
	if err := requireOneRow(result, "card attempt settlement"); err != nil {
		return false, nil, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE card_charges SET state = ?, payment_intent = ?, closed_at = ?
     WHERE team = ? AND id = ? AND state = ?`, CardChargePaid, p.PaymentIntent, nowNS, team, p.ChargeID, CardChargeOpen)
	if err != nil {
		return false, nil, err
	}
	if err := requireOneRow(result, "card charge settlement"); err != nil {
		return false, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE credit_freezes SET released_at = ?
	    WHERE team = ? AND cause = ? AND released_at IS NULL`, nowNS, string(team), FreezeCauseDecline); err != nil {
		return false, nil, err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardCharged, SubjectID: p.PaymentIntent, Actor: "card", At: now,
		Attrs: map[string]any{"charge_id": p.ChargeID, "attempt_id": p.AttemptID, "amount_micro": amount},
	}); err != nil {
		return false, nil, err
	}
	room, err := headroomTx(ctx, tx, team, tx.now())
	if err != nil {
		return false, nil, err
	}
	if room > 0 {
		if err := clearTeamCreditExhaustedTx(ctx, tx, team, nowNS); err != nil {
			return false, nil, err
		}
	}
	// safety: partial settlement must retain an open obligation for the unpaid debt.
	if err := decideCardChargeTx(ctx, tx, team, now); err != nil {
		return false, nil, err
	}
	return true, warned, nil
}

func failCardAttemptTx(ctx context.Context, tx *storeTx, attemptID, paymentIntent, declineCode string, now time.Time) (team Team, first bool, err error) {
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return "", false, err
	}
	var chargeID, status, name string
	err = tx.QueryRowContext(ctx, `SELECT charge_id, status, team FROM card_attempts WHERE id = ?`, attemptID).
		Scan(&chargeID, &status, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("%w: card attempt %q", ErrNotFound, attemptID)
	}
	if err != nil || status != CardAttemptLive {
		return Team(name), false, err
	}
	nowNS := now.UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, payment_intent = ?, decline_code = ?,
	    updated_at = ? WHERE id = ?`, CardAttemptFailed, paymentIntent, declineCode, nowNS, attemptID); err != nil {
		return "", false, err
	}
	var failures int64
	if err := tx.QueryRowContext(ctx, `SELECT failures FROM card_charges WHERE id = ? AND state = ?`,
		chargeID, CardChargeOpen).Scan(&failures); errors.Is(err, sql.ErrNoRows) {
		return Team(name), false, nil
	} else if err != nil {
		return "", false, err
	}
	next := int64(1<<63 - 1)
	if int(failures) < len(cardRetryAfter) {
		next = now.Add(cardRetryAfter[failures]).UnixNano()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_charges SET failures = failures + 1, decline_code = ?,
	    next_attempt_at = ? WHERE id = ?`, declineCode, next, chargeID); err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, cause, created_at)
	    VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (dispute_id) DO NOTHING`, "decline-"+chargeID, name, paymentIntent,
		"card charge declined: "+declineCode, FreezeCauseDecline, nowNS); err != nil {
		return "", false, err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: Team(name), Kind: BusinessEventCardFailed, SubjectID: attemptID, Actor: "card", At: now,
		Attrs: map[string]any{"charge_id": chargeID, "decline_code": declineCode, "failures": failures + 1},
	}); err != nil {
		return "", false, err
	}
	return Team(name), failures == 0, nil
}

func reportCardRefundTx(ctx context.Context, tx *storeTx, key, refundID, status string, now time.Time) (bool, error) {
	paymentIntent, attempt, ok := parseRefundKey(key)
	if !ok {
		return false, nil
	}
	switch status {
	case CardRefundSucceeded, "failed", "canceled":
	default:
		return false, nil
	}
	var team Team
	err := tx.QueryRowContext(ctx, `SELECT team FROM card_refunds WHERE payment_intent = ?`, paymentIntent).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return applyCardRefundTx(ctx, tx, team, paymentIntent, refundID, status, `attempts = ?`, []any{attempt}, now)
}

func applyCardRefundTx(ctx context.Context, tx *storeTx, team Team, paymentIntent, refundID, status, when string, whenArgs []any, now time.Time) (bool, error) {
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return false, err
	}
	var attempts int64
	args := append([]any{team, paymentIntent}, whenArgs...)
	err := tx.QueryRowContext(ctx, `SELECT attempts FROM card_refunds WHERE team = ? AND payment_intent = ? AND `+when+tx.forUpdate(), args...).Scan(&attempts)
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
	args = append(append(args, string(team), paymentIntent), whenArgs...)
	res, err := tx.ExecContext(ctx, `UPDATE card_refunds SET `+set+` WHERE team = ? AND payment_intent = ? AND `+when, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
