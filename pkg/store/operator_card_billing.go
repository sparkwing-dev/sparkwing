package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DueCardCharges opens deployment charges and starts due collection attempts.
func (o *Operator) DueCardCharges(ctx context.Context, now time.Time) (_ []CardChargeWork, _ []BudgetAlert, err error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return nil, nil, err
	}
	nowNS := now.UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, decline_code = 'expired', updated_at = ?
	    WHERE status = ? AND kind = ? AND created_at < ?`, CardAttemptFailed, nowNS, CardAttemptLive,
		CardAttemptRecovery, now.Add(-recoveryAttemptLife).UnixNano()); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, decline_code = 'key_expired', updated_at = ?
	    WHERE status = ? AND kind = ? AND payment_intent = '' AND created_at < ?`, CardAttemptFailed, nowNS,
		CardAttemptLive, CardAttemptOffSession, now.Add(-cardKeyLife).UnixNano()); err != nil {
		return nil, nil, err
	}
	teams, err := billedTeamsTx(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	var alerts []BudgetAlert
	for _, team := range teams {
		if team.carded {
			if err := decideCardChargeTx(ctx, tx, team.name, now); err != nil {
				return nil, nil, err
			}
		}
		alert, err := budgetAlertTx(ctx, tx, team.name, now)
		if err != nil {
			return nil, nil, err
		}
		if alert != nil {
			alerts = append(alerts, *alert)
		}
	}
	work, err := startDueAttemptsTx(ctx, tx, now)
	if err != nil {
		return nil, nil, err
	}
	return work, alerts, tx.Commit()
}

// RecordAttemptIntent binds a live attempt to its provider payment identity.
func (o *Operator) RecordAttemptIntent(ctx context.Context, attemptID, paymentIntent string, now time.Time) error {
	if attemptID == "" || paymentIntent == "" {
		return fmt.Errorf("%w: an attempt's intent names both", ErrInvalidInput)
	}
	res, err := o.s.exec(ctx, `UPDATE card_attempts SET payment_intent = ?, updated_at = ?
	    WHERE id = ? AND status = ? AND (payment_intent = '' OR payment_intent = ?)`,
		paymentIntent, now.UnixNano(), attemptID, CardAttemptLive, paymentIntent)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmp.Or(err, fmt.Errorf("%w: attempt %q is not live or has another payment", ErrInvalidInput, attemptID))
	}
	return nil
}

// SettleCardPayment settles a provider payment against its persisted card attempt.
func (o *Operator) SettleCardPayment(ctx context.Context, p CardPayment, now time.Time) (created bool, warned *WarnedCardPayment, err error) {
	if p.PaymentIntent == "" || p.AmountCents <= 0 || p.ChargeID == "" || p.AttemptID == "" {
		return false, nil, fmt.Errorf("%w: a card payment names its charge, attempt, payment intent and amount", ErrInvalidInput)
	}
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return false, nil, err
	}
	defer rollbackUnlessDone(tx, &err)
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
	if err := tx.QueryRowContext(ctx, `SELECT state, amount_micro FROM card_charges WHERE id = ?`, p.ChargeID).
		Scan(&state, &frozenAmount); err != nil {
		return false, nil, err
	}
	nowNS := now.UnixNano()
	amount := p.AmountCents * MicroCreditsPerCent
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
			return false, warned, tx.Commit()
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
		return true, nil, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, payment_intent = ?, updated_at = ?
	    WHERE id = ?`, CardAttemptPaid, p.PaymentIntent, nowNS, p.AttemptID); err != nil {
		return false, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_charges SET state = ?, payment_intent = ?, closed_at = ?
	    WHERE id = ?`, CardChargePaid, p.PaymentIntent, nowNS, p.ChargeID); err != nil {
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
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil {
		return false, nil, err
	}
	if balance > 0 {
		if err := clearTeamCreditExhaustedTx(ctx, tx, team, nowNS); err != nil {
			return false, nil, err
		}
	}
	// safety: what is still owed opens the next charge in this transaction,
	// so a payment that settles less than the debt never leaves it unbilled.
	if err := decideCardChargeTx(ctx, tx, team, now); err != nil {
		return false, nil, err
	}
	return true, warned, tx.Commit()
}

// RecordPaymentWarning records provider fraud evidence and holds its persisted payer.
func (o *Operator) RecordPaymentWarning(ctx context.Context, w PaymentWarning, now time.Time) (_ Team, err error) {
	if w.WarningID == "" || w.PaymentIntent == "" {
		return "", fmt.Errorf("%w: a warning names itself and its payment", ErrInvalidInput)
	}
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return "", err
	}
	actionable := 0
	if w.Actionable {
		actionable = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO payment_warnings (warning_id, payment_intent, fingerprint, actionable,
	    created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (warning_id) DO NOTHING`,
		w.WarningID, w.PaymentIntent, w.Fingerprint, actionable, now.UnixNano()); err != nil {
		return "", err
	}
	if !w.Actionable {
		return "", tx.Commit()
	}
	team, err := paymentWarningTeamTx(ctx, tx, w.PaymentIntent)
	if err != nil {
		return "", err
	}
	if team == "" {
		return "", tx.Commit()
	}
	if err := holdForWarningTx(ctx, tx, team, w.WarningID, w.PaymentIntent, now); err != nil {
		return "", err
	}
	return team, tx.Commit()
}

// DueCardRefunds lists due refunds across the deployment.
func (o *Operator) DueCardRefunds(ctx context.Context, now time.Time) (_ []CardRefundWork, err error) {
	rows, err := o.s.query(ctx, `SELECT team, payment_intent, reason, attempts FROM card_refunds
	    WHERE status = ? AND next_attempt_at <= ? ORDER BY created_at`, CardRefundDue, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CardRefundWork
	for rows.Next() {
		var w CardRefundWork
		var attempts int64
		if err := rows.Scan(&w.Team, &w.PaymentIntent, &w.Reason, &attempts); err != nil {
			return nil, err
		}
		w.Key = fmt.Sprintf("%s-%d", w.PaymentIntent, attempts)
		out = append(out, w)
	}
	return out, rows.Err()
}

// MarkCardRefundMade records the provider response for an exact queued refund attempt.
func (o *Operator) MarkCardRefundMade(
	ctx context.Context, team Team, key, refundID, status string, now time.Time,
) error {
	paymentIntent, attempt, ok := parseRefundKey(key)
	if !ok {
		return fmt.Errorf("%w: refund key %q", ErrInvalidInput, key)
	}
	_, err := o.applyCardRefund(ctx, team, paymentIntent, refundID, status,
		`attempts = ? AND status = 'due'`, []any{attempt}, now)
	return err
}

// ReportCardRefund applies a provider refund outcome to its current queued attempt.
func (o *Operator) ReportCardRefund(ctx context.Context, key, refundID, status string, now time.Time) (bool, error) {
	paymentIntent, attempt, ok := parseRefundKey(key)
	if !ok {
		return false, nil
	}
	var team string
	err := o.s.queryRow(ctx, `SELECT team FROM card_refunds WHERE payment_intent = ?`, paymentIntent).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// safety: only the current attempt's key moves the queue, even before the
	// worker binds the refund; a failure moves it to the next attempt, so a
	// repeat or a stale attempt matches nothing.
	switch status {
	case CardRefundSucceeded, "failed", "canceled":
		return o.applyCardRefund(ctx, Team(team), paymentIntent, refundID, status, `attempts = ?`, []any{attempt}, now)
	}
	return false, nil
}

// FailCardAttempt records a failed collection attempt and its payer hold.
func (o *Operator) FailCardAttempt(
	ctx context.Context, attemptID, paymentIntent, declineCode string, now time.Time,
) (team Team, first bool, err error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return "", false, err
	}
	defer rollbackUnlessDone(tx, &err)
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
		return Team(name), false, tx.Commit()
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
	// safety: the hold is keyed by the charge, so every retry of it shares
	// one hold, and paying the charge releases it.
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
	return Team(name), failures == 0, tx.Commit()
}

// DropCardAttempt closes a live collection attempt that never reached the provider.
func (o *Operator) DropCardAttempt(ctx context.Context, attemptID string, now time.Time) error {
	_, err := o.s.exec(ctx, `UPDATE card_attempts SET status = ?, decline_code = 'not_opened', updated_at = ?
	    WHERE id = ? AND status = ?`, CardAttemptFailed, now.UnixNano(), attemptID, CardAttemptLive)
	return err
}

func (o *Operator) applyCardRefund(
	ctx context.Context, team Team, paymentIntent, refundID, status, when string, whenArgs []any, now time.Time,
) (bool, error) {
	set, args := `refund_id = ?, status = ?`, []any{refundID, CardRefundPending}
	switch status {
	case CardRefundSucceeded:
		args[1] = CardRefundSucceeded
	case "failed", "canceled":
		// safety: a refund can fail after it succeeded; its reversal is capped
		// at what the payment still holds, so retrying cannot take back twice.
		set = `refund_id = ?, status = ?, attempts = attempts + 1, next_attempt_at = ?`
		args = []any{refundID, CardRefundDue, now.Add(cardRefundBackoff(ctx, o.s, team, paymentIntent)).UnixNano()}
	}
	args = append(append(args, string(team), paymentIntent), whenArgs...)
	res, err := o.s.exec(ctx, `UPDATE card_refunds SET `+set+` WHERE team = ? AND payment_intent = ? AND `+when, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
