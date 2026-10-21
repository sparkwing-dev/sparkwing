package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (s *Store) settlePaymentFacts(ctx context.Context, event capability.PaymentEvent) (PaymentSettlement, error) {
	if err := validatePaymentEvent(event); err != nil {
		return PaymentSettlement{}, err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return PaymentSettlement{}, err
	}
	digest, err := paymentEventDigest(event)
	if err != nil {
		return PaymentSettlement{}, err
	}
	var result PaymentSettlement
	err = s.inTx(ctx, func(tx *storeTx) error {
		if err := lockPaymentTx(ctx, tx, event.Mode, paymentStateID(event)); err != nil {
			return err
		}
		if err := configuredPaymentModeTx(ctx, tx, event.Mode); err != nil {
			return err
		}
		replayed, err := paymentEventReplayTx(ctx, tx, event, digest)
		if err != nil {
			return err
		}
		if replayed {
			var encoded []byte
			if err := tx.QueryRowContext(ctx, "SELECT settlement_json FROM payment_events WHERE payment_mode=? AND event_id=?", event.Mode, event.EventID).Scan(&encoded); err != nil {
				return err
			}
			if err := json.Unmarshal(encoded, &result); err != nil {
				return err
			}
			result.Replayed = true
			return nil
		}
		if event.ObjectID != "" {
			if err := checkPaymentObjectTx(ctx, tx, event); err != nil {
				return err
			}
		}
		result, err = tx.operator.applyPaymentStateTx(ctx, tx, event, "payment_provider")
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		var checkoutID any
		if event.CheckoutID != "" {
			checkoutID = result.CheckoutID
		}
		clockTime1, clockErr1 := tx.now(ctx)
		if clockErr1 != nil {
			return clockErr1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO payment_events(team,payment_mode,event_id,checkout_id,payment_id,event_kind,event_digest,grant_id,processed_at,object_id,event_json,settlement_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, optionalString(string(result.Team)), event.Mode, event.EventID, checkoutID, event.PaymentID, event.Kind, digest, optionalString(result.GrantID), clockTime1.UnixNano(), event.ObjectID, string(body), string(encoded))
		return err
	})
	if err != nil {
		return PaymentSettlement{}, err
	}
	return result, nil
}

func paymentStateID(e capability.PaymentEvent) string {
	if e.PaymentID != "" {
		return e.PaymentID
	}
	if e.IntentID != "" {
		return e.IntentID
	}
	if e.CheckoutID != "" {
		return e.CheckoutID
	}
	return e.ObjectID
}

func lockPaymentTx(ctx context.Context, tx *storeTx, mode capability.PaymentMode, id string) error {
	return advisoryLockTx(ctx, tx, "sparkwing/payment/"+string(mode)+"/"+id)
}

func validatePaymentEvent(e capability.PaymentEvent) error {
	if e.Kind == capability.PaymentCheckoutPaid || e.Kind == capability.PaymentCheckoutExpired || e.Kind == capability.PaymentCheckoutFailed {
		if e.EventID == "" || e.CheckoutID == "" || e.AmountMinor <= 0 || e.Currency == "" || (e.Mode != capability.PaymentTest && e.Mode != capability.PaymentLive) || e.Kind == capability.PaymentCheckoutPaid && e.PaymentID == "" {
			return authorityError(apierr.BadRequest, "invalid verified payment facts")
		}
		return nil
	}
	return validateProviderPayment(e)
}

func validateProviderPayment(e capability.PaymentEvent) error {
	if e.EventID == "" || e.ObjectID == "" || e.CheckoutID != "" || e.Created <= 0 || e.Created > math.MaxInt64/int64(time.Second) || (e.Mode != capability.PaymentTest && e.Mode != capability.PaymentLive) {
		return authorityError(apierr.BadRequest, "invalid verified provider facts")
	}
	switch e.Kind {
	case capability.PaymentCardSetup:
		return nil
	case capability.PaymentPageCompleted:
		if e.IntentID != "" && e.CustomerID != "" {
			return nil
		}
	case capability.PaymentWarningEvent:
		if e.PaymentID != "" {
			return nil
		}
	case capability.PaymentCardPaid, capability.PaymentCardFailed, capability.PaymentRefund, capability.PaymentDisputeOpened, capability.PaymentDisputeClosed:
		if e.PaymentID != "" && e.AmountMinor > 0 && e.Currency == "usd" {
			_, err := billingProduct(e.AmountMinor, MicroCreditsPerCent)
			return err
		}
	}
	return authorityError(apierr.BadRequest, "unsupported or incomplete provider outcome")
}

func checkPaymentObjectTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent) error {
	rows, err := tx.QueryContext(ctx, "SELECT event_json FROM payment_events WHERE payment_mode=? AND object_id=?", event.Mode, event.ObjectID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return errors.Join(err, rows.Close())
		}
		var previous capability.PaymentEvent
		if err := json.Unmarshal(encoded, &previous); err != nil {
			return errors.Join(err, rows.Close())
		}
		if !samePaymentObject(previous, event) {
			return errors.Join(authorityError(apierr.Conflict, "provider object changed its committed association"), rows.Close())
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}

func samePaymentObject(a, b capability.PaymentEvent) bool {
	family := func(kind capability.PaymentEventKind) capability.PaymentEventKind {
		switch kind {
		case capability.PaymentCardFailed:
			return capability.PaymentCardPaid
		case capability.PaymentDisputeClosed:
			return capability.PaymentDisputeOpened
		default:
			return kind
		}
	}
	if family(a.Kind) != family(b.Kind) || a.PaymentID != b.PaymentID || a.IntentID != b.IntentID || a.CustomerID != "" && b.CustomerID != "" && a.CustomerID != b.CustomerID {
		return false
	}
	if family(a.Kind) == capability.PaymentCardPaid && (a.Kind == capability.PaymentCardFailed || b.Kind == capability.PaymentCardFailed) {
		return true
	}
	if a.AmountMinor != b.AmountMinor || a.Currency != b.Currency || a.ChargeID != b.ChargeID {
		return false
	}
	for _, values := range [][2]string{{a.CustomerID, b.CustomerID}, {a.MethodID, b.MethodID}, {a.Fingerprint, b.Fingerprint}} {
		if values[0] != "" && values[1] != "" && values[0] != values[1] {
			return false
		}
	}
	if a.Kind == capability.PaymentCardSetup && (a.Brand != b.Brand || a.Last4 != b.Last4) {
		return false
	}
	return a.Kind != capability.PaymentDisputeClosed || b.Kind != capability.PaymentDisputeClosed || a.Status == b.Status
}

func (op *Operator) applyPaymentStateTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent, actor string) (PaymentSettlement, error) {
	// The refill worker locks ledger before checkout; callbacks preserve that order after the payment lock.
	var team Team
	err := tx.QueryRowContext(ctx, `SELECT team FROM credit_checkouts WHERE payment_mode=? AND ((payment_id<>'' AND payment_id=?) OR (session_id<>id AND session_id=?))`, e.Mode, e.PaymentID, e.CheckoutID).Scan(&team)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PaymentSettlement{}, err
	}
	if team != "" {
		if _, err := lockLedgerTx(ctx, tx, team); err != nil {
			return PaymentSettlement{}, err
		}
	}
	if e.Kind == capability.PaymentCardPaid || e.Kind == capability.PaymentCardFailed {
		encoded, err := json.Marshal(e)
		if err != nil {
			return PaymentSettlement{}, err
		}
		status := "failed"
		if e.Kind == capability.PaymentCardPaid {
			status = "succeeded"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO payment_states(payment_mode,payment_id,status,outcome_json) VALUES (?,?,?,?) ON CONFLICT(payment_mode,payment_id) DO UPDATE SET status=excluded.status,outcome_json=excluded.outcome_json WHERE payment_states.status<>'succeeded'`, e.Mode, e.PaymentID, status, string(encoded)); err != nil {
			return PaymentSettlement{}, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT outcome_json FROM payment_states WHERE payment_mode=? AND payment_id=?`, e.Mode, e.PaymentID).Scan(&encoded); err != nil {
			return PaymentSettlement{}, err
		}
		if err := json.Unmarshal(encoded, &e); err != nil {
			return PaymentSettlement{}, err
		}
	}
	result, err := tx.operator.reducePaymentStateTx(ctx, tx, e)
	if err != nil {
		return result, err
	}
	if e.Kind == capability.PaymentCardSetup || e.Kind == capability.PaymentPageCompleted {
		intent := e.ObjectID
		if e.Kind == capability.PaymentPageCompleted {
			intent = e.IntentID
		}
		team, err := tx.operator.reconcileSetupStateTx(ctx, tx, e.Mode, intent)
		if err != nil {
			return result, err
		}
		if team != "" {
			result.Team = team
		}
	}
	if e.PaymentID == "" {
		return result, nil
	}
	if err := tx.operator.reconcilePaymentStateTx(ctx, tx, e.Mode, e.PaymentID, paymentAttribution{refund: e.ObjectID, actor: actor}); err != nil {
		return result, err
	}
	if result.Team == "" {
		result.Team, err = tx.operator.paymentTeamTx(ctx, tx, e.Mode, e.PaymentID)
		if err != nil {
			return result, err
		}
	}
	if e.Kind == capability.PaymentRefund || e.Kind == capability.PaymentDisputeClosed {
		kind := "dispute"
		if e.Kind == capability.PaymentRefund {
			kind = "refund"
		}
		reference := paymentConsequenceReference(e.Mode, kind, e.ObjectID)
		reversal, found, err := creditGrantByReferenceTx(ctx, tx, result.Team, CreditGrantReversal, reference)
		if err != nil {
			return result, err
		}
		if found {
			result.GrantID, result.AmountMicro = reversal.ID, reversal.AmountMicro
		}
	}
	return result, nil
}

func (op *Operator) reducePaymentStateTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) (PaymentSettlement, error) {
	var result PaymentSettlement
	switch e.Kind {
	case capability.PaymentCheckoutPaid, capability.PaymentCheckoutExpired, capability.PaymentCheckoutFailed:
		return tx.operator.applyCheckoutPaymentTx(ctx, tx, e)
	case capability.PaymentPageCompleted:
		return tx.operator.bindCompletedPageTx(ctx, tx, e)
	case capability.PaymentCardSetup:
		if e.CustomerID == "" || e.MethodID == "" || e.Fingerprint == "" {
			return result, ErrInvalidInput
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO payment_setups(payment_mode,intent_id,customer_id,payment_method,fingerprint,brand,last4,created_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(payment_mode,intent_id) DO NOTHING`, e.Mode, e.ObjectID, e.CustomerID, e.MethodID, e.Fingerprint, e.Brand, e.Last4, e.Created)
		if err == nil {
			n, rowsErr := res.RowsAffected()
			result.Replayed = n == 0
			err = rowsErr
		}
		return result, err
	case capability.PaymentCardPaid, capability.PaymentCardFailed:
		var checkout paymentCheckout
		var customer string
		err := tx.QueryRowContext(ctx, `SELECT id,team,kind,payment_id,currency,amount_minor,amount_micro,paid_at,closed_at,grant_id,customer_id FROM credit_checkouts WHERE payment_mode=? AND payment_id=? AND kind=?`+tx.forUpdate(), e.Mode, e.PaymentID, CheckoutKindRefill).Scan(append(checkout.scan(), &customer)...)
		if errors.Is(err, sql.ErrNoRows) {
			result.Payment = e.PaymentID
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if checkout.currency != e.Currency || e.CustomerID != customer || e.Kind == capability.PaymentCardPaid && checkout.amount != e.AmountMinor {
			return result, authorityError(apierr.Conflict, "payment facts conflict with committed refill")
		}
		checkout.result.Payment, checkout.result.GrantID = e.PaymentID, checkout.grant.String
		checkout.result.Replayed = checkout.paid.Valid
		err = applyPaymentOutcomeTx(ctx, tx, e, &checkout)
		return checkout.result, err
	case capability.PaymentWarningEvent:
		w := PaymentWarning{WarningID: e.ObjectID, PaymentIntent: e.PaymentID, Fingerprint: e.Fingerprint, Actionable: e.Actionable}
		now, clockErr2 := tx.now(ctx)
		if clockErr2 != nil {
			return PaymentSettlement{}, clockErr2
		}
		if w.WarningID == "" || w.PaymentIntent == "" {
			return result, fmt.Errorf("%w: a warning names itself and its payment", ErrInvalidInput)
		}
		actionable := 0
		if w.Actionable {
			actionable = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO payment_warnings (payment_mode, warning_id, payment_intent, fingerprint, actionable,
	    created_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (payment_mode,warning_id) DO NOTHING`,
			e.Mode, w.WarningID, w.PaymentIntent, w.Fingerprint, actionable, now.UnixNano()); err != nil {
			return result, err
		}
		var payment, fingerprint string
		if err := tx.QueryRowContext(ctx, `SELECT payment_intent,fingerprint FROM payment_warnings WHERE payment_mode=? AND warning_id=?`, e.Mode, w.WarningID).Scan(&payment, &fingerprint); err != nil {
			return result, err
		}
		if payment != w.PaymentIntent || fingerprint != w.Fingerprint {
			return result, fmt.Errorf("%w: warning identity names another payment or card", ErrCreditGrantConflict)
		}
		if !w.Actionable {
			return result, nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE payment_warnings SET actionable=1 WHERE payment_mode=? AND warning_id=?", e.Mode, w.WarningID); err != nil {
			return result, err
		}
		if err := forgetCardTx(ctx, tx, fingerprint, CardRefusedWarning, now); err != nil {
			return result, err
		}
		result.Team, result.Payment = "", payment
		return result, nil
	case capability.PaymentRefund:
		result.Payment = e.PaymentID
		return result, recordPaymentConsequenceTx(ctx, tx, e, "refund")
	case capability.PaymentDisputeOpened, capability.PaymentDisputeClosed:
		result.Payment = e.PaymentID
		if e.Kind == capability.PaymentDisputeOpened {
			e.Status = CardRefundPending
		} else if e.Status == "lost" {
			e.Status = CardRefundSucceeded
		} else {
			e.Status = "canceled"
		}
		return result, recordPaymentConsequenceTx(ctx, tx, e, "dispute")

	}
	return result, authorityError(apierr.BadRequest, "unsupported provider outcome")
}

func (op *Operator) bindCompletedPageTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) (PaymentSettlement, error) {
	var team, customer string
	err := tx.QueryRowContext(ctx, `SELECT team,customer_id FROM payment_correlations WHERE payment_mode=? AND provider_id=? AND kind='setup_session'`+tx.forShare(), e.Mode, e.ObjectID).Scan(&team, &customer)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentSettlement{}, authorityError(apierr.NotFound, "committed payment page is unavailable")
	}
	if err != nil {
		return PaymentSettlement{}, err
	}
	if customer != e.CustomerID {
		return PaymentSettlement{}, authorityError(apierr.Conflict, "completed page names another customer")
	}
	result := PaymentSettlement{Team: Team(team)}
	var bound string
	err = tx.QueryRowContext(ctx, `SELECT team FROM payment_correlations WHERE payment_mode=? AND provider_id=? AND kind='setup' AND customer_id=?`, e.Mode, e.IntentID, customer).Scan(&bound)
	switch {
	case err == nil && bound == team:
		return result, nil
	case err == nil:
		return PaymentSettlement{}, authorityError(apierr.Conflict, "setup intent belongs to another team")
	case !errors.Is(err, sql.ErrNoRows):
		return PaymentSettlement{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES (?,?,'setup',?,?)`, e.Mode, e.IntentID, team, customer)
	return result, err
}

// RecordsPaymentParent reports whether this deployment committed the checkout, refill, page or intent an event concerns.
func (o *Operator) RecordsPaymentParent(ctx context.Context, r capability.PaymentRoute) (bool, error) {
	var found int
	err := o.s.queryRow(ctx, `SELECT CASE WHEN
 EXISTS(SELECT 1 FROM credit_checkouts WHERE payment_mode=? AND (session_id=? OR payment_id<>'' AND payment_id=?))
 OR EXISTS(SELECT 1 FROM payment_correlations WHERE payment_mode=? AND provider_id IN (?,?,?))
 THEN 1 ELSE 0 END`, r.Mode, r.CheckoutID, r.PaymentID, r.Mode, r.CheckoutID, r.PaymentID, r.ObjectID).Scan(&found)
	return found == 1, err
}

func (op *Operator) reconcileSetupStateTx(ctx context.Context, tx *storeTx, mode capability.PaymentMode, intent string) (Team, error) {
	var card Card
	var created int64
	var applied int
	err := tx.QueryRowContext(ctx, `SELECT customer_id,payment_method,fingerprint,brand,last4,created_at,applied FROM payment_setups WHERE payment_mode=? AND intent_id=?`, mode, intent).Scan(&card.Customer, &card.PaymentMethod, &card.Fingerprint, &card.Brand, &card.Last4, &created, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var team Team
	var customer string
	err = tx.QueryRowContext(ctx, `SELECT team,customer_id FROM payment_correlations WHERE payment_mode=? AND provider_id=? AND kind='setup'`, mode, intent).Scan(&team, &customer)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if customer != card.Customer {
		return "", authorityError(apierr.Conflict, "setup names another customer")
	}
	if applied != 0 {
		return team, nil
	}
	card.AddedAt = time.Unix(created, 0)
	clockTime3, clockErr3 := tx.now(ctx)
	if clockErr3 != nil {
		return "", clockErr3
	}
	if err := saveCardTx(ctx, tx, team, card, "payment_provider", clockTime3); err != nil && !errors.Is(err, ErrStaleCardSetup) {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE payment_setups SET applied=1 WHERE payment_mode=? AND intent_id=?`, mode, intent)
	return team, err
}
